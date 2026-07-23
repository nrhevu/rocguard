package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Resolver interface {
	ResolveDockerContainer(ctx context.Context, nameOrID string) (string, error)
	DockerContainerName(ctx context.Context, containerID string) (string, error)
	InspectPodmanContainer(ctx context.Context, uid int, nameOrID string) (PodmanContainer, error)
	NamespaceForContainer(ctx context.Context, containerID string) (string, error)
}

var ErrNotFound = errors.New("runtime object not found")

type PodmanContainer struct {
	ID         string
	Name       string
	CgroupPath string
}

type CLIResolver struct {
	Timeout time.Duration
}

type cacheEntry struct {
	value     any
	err       error
	expiresAt time.Time
}

type resolveCall struct {
	done  chan struct{}
	value any
	err   error
}

type CachedResolver struct {
	Base Resolver
	TTL  time.Duration

	mu              sync.Mutex
	dockerIDs       map[string]cacheEntry
	dockerNames     map[string]cacheEntry
	podman          map[string]cacheEntry
	namespaces      map[string]cacheEntry
	dockerIDCalls   map[string]*resolveCall
	dockerNameCalls map[string]*resolveCall
	podmanCalls     map[string]*resolveCall
	namespaceCalls  map[string]*resolveCall
	now             func() time.Time
}

const (
	maxResolverCacheEntries = 1024
	maxRuntimeOutputBytes   = 64 << 20
	maxRuntimeErrorBytes    = 64 << 10
)

type commandError struct {
	name           string
	err            error
	stderr         string
	stderrExceeded bool
}

func (e *commandError) Error() string { return fmt.Sprintf("%s command failed: %v", e.name, e.err) }
func (e *commandError) Unwrap() error { return e.err }

type boundedOutput struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (b *boundedOutput) Write(data []byte) (int, error) {
	original := len(data)
	remaining := b.limit - b.Len()
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = b.Buffer.Write(data)
	}
	if original > remaining {
		b.exceeded = true
	}
	return original, nil
}

func NewCachedResolver(base Resolver) *CachedResolver {
	return &CachedResolver{
		Base:      base,
		dockerIDs: make(map[string]cacheEntry), dockerNames: make(map[string]cacheEntry), podman: make(map[string]cacheEntry), namespaces: make(map[string]cacheEntry),
		dockerIDCalls: make(map[string]*resolveCall), dockerNameCalls: make(map[string]*resolveCall), podmanCalls: make(map[string]*resolveCall), namespaceCalls: make(map[string]*resolveCall),
		now: time.Now,
	}
}

func (r *CachedResolver) ResolveDockerContainer(ctx context.Context, nameOrID string) (string, error) {
	key := strings.TrimSpace(nameOrID)
	value, err := r.resolve(ctx, r.dockerIDs, r.dockerIDCalls, key, func() (any, error) {
		return r.Base.ResolveDockerContainer(ctx, nameOrID)
	})
	if err != nil {
		return "", err
	}
	return value.(string), nil
}

func (r *CachedResolver) DockerContainerName(ctx context.Context, containerID string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(containerID))
	value, err := r.resolve(ctx, r.dockerNames, r.dockerNameCalls, key, func() (any, error) {
		return r.Base.DockerContainerName(ctx, containerID)
	})
	if err != nil {
		return "", err
	}
	return value.(string), nil
}

func (r *CachedResolver) InspectPodmanContainer(ctx context.Context, uid int, nameOrID string) (PodmanContainer, error) {
	key := strconv.Itoa(uid) + ":" + strings.TrimSpace(nameOrID)
	value, err := r.resolve(ctx, r.podman, r.podmanCalls, key, func() (any, error) {
		return r.Base.InspectPodmanContainer(ctx, uid, nameOrID)
	})
	if err != nil {
		return PodmanContainer{}, err
	}
	return value.(PodmanContainer), nil
}

func (r *CachedResolver) NamespaceForContainer(ctx context.Context, containerID string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(containerID))
	value, err := r.resolve(ctx, r.namespaces, r.namespaceCalls, key, func() (any, error) {
		return r.Base.NamespaceForContainer(ctx, containerID)
	})
	if err != nil {
		return "", err
	}
	return value.(string), nil
}

func (r *CachedResolver) resolve(ctx context.Context, cache map[string]cacheEntry, calls map[string]*resolveCall, key string, call func() (any, error)) (any, error) {
	if r == nil || r.Base == nil {
		return nil, errors.New("runtime resolver is unavailable")
	}
	now := r.nowTime()
	r.mu.Lock()
	entry, found := cache[key]
	if found && now.Before(entry.expiresAt) {
		r.mu.Unlock()
		return entry.value, entry.err
	}
	if found {
		delete(cache, key)
	}
	if pending := calls[key]; pending != nil {
		r.mu.Unlock()
		select {
		case <-pending.done:
			return pending.value, pending.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	pending := &resolveCall{done: make(chan struct{})}
	calls[key] = pending
	r.mu.Unlock()

	value, err := call()
	completedAt := r.nowTime()
	r.mu.Lock()
	if ctx.Err() == nil && len(cache) >= maxResolverCacheEntries {
		for candidate, candidateEntry := range cache {
			if !completedAt.Before(candidateEntry.expiresAt) {
				delete(cache, candidate)
			}
		}
		for len(cache) >= maxResolverCacheEntries {
			for candidate := range cache {
				delete(cache, candidate)
				break
			}
		}
	}
	if ctx.Err() == nil {
		cache[key] = cacheEntry{value: value, err: err, expiresAt: completedAt.Add(r.ttl())}
	}
	pending.value = value
	pending.err = err
	delete(calls, key)
	close(pending.done)
	r.mu.Unlock()
	return value, err
}

func (r *CachedResolver) ttl() time.Duration {
	if r.TTL > 0 {
		return r.TTL
	}
	return 5 * time.Second
}

func (r *CachedResolver) nowTime() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

func (r CLIResolver) ResolveDockerContainer(ctx context.Context, nameOrID string) (string, error) {
	nameOrID = strings.TrimSpace(nameOrID)
	if nameOrID == "" {
		return "", errors.New("container name/id is required")
	}
	ctx, cancel := r.withTimeout(ctx)
	defer cancel()
	out, err := limitedCommandOutput(ctx, "docker", "inspect", "--format", "{{.Id}}", nameOrID)
	if err != nil {
		if commandReportsNotFound(err) {
			return "", fmt.Errorf("%w: docker container", ErrNotFound)
		}
		return "", err
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return "", errors.New("docker inspect returned empty container id")
	}
	return strings.ToLower(id), nil
}

func (r CLIResolver) DockerContainerName(ctx context.Context, containerID string) (string, error) {
	containerID = strings.TrimSpace(containerID)
	if containerID == "" {
		return "", errors.New("container id is empty")
	}
	ctx, cancel := r.withTimeout(ctx)
	defer cancel()
	out, err := limitedCommandOutput(ctx, "docker", "inspect", "--format", "{{.Name}}", containerID)
	if err != nil {
		if commandReportsNotFound(err) {
			return "", fmt.Errorf("%w: docker container", ErrNotFound)
		}
		return "", err
	}
	name := strings.TrimPrefix(strings.TrimSpace(string(out)), "/")
	if name == "" {
		return "", errors.New("docker inspect returned empty container name")
	}
	return name, nil
}

func (r CLIResolver) InspectPodmanContainer(ctx context.Context, uid int, nameOrID string) (PodmanContainer, error) {
	nameOrID = strings.TrimSpace(nameOrID)
	if nameOrID == "" {
		return PodmanContainer{}, errors.New("container name/id is required")
	}
	if uid < 0 {
		return PodmanContainer{}, errors.New("podman owner uid is invalid")
	}
	ctx, cancel := r.withTimeout(ctx)
	defer cancel()
	out, err := limitedCommandOutputAs(ctx, uid, "podman", "container", "inspect", nameOrID)
	if err != nil {
		if commandReportsNotFound(err) {
			return PodmanContainer{}, fmt.Errorf("%w: podman container", ErrNotFound)
		}
		return PodmanContainer{}, err
	}
	var rows []struct {
		ID    string `json:"Id"`
		Name  string `json:"Name"`
		State struct {
			CgroupPath string `json:"CgroupPath"`
		} `json:"State"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return PodmanContainer{}, fmt.Errorf("decode podman inspect: %w", err)
	}
	if len(rows) != 1 {
		return PodmanContainer{}, fmt.Errorf("podman inspect returned %d containers", len(rows))
	}
	id := strings.ToLower(strings.TrimSpace(rows[0].ID))
	name := strings.TrimSpace(rows[0].Name)
	if len(id) != 64 || !isHex(id) {
		return PodmanContainer{}, errors.New("podman inspect returned invalid container id")
	}
	if name == "" {
		return PodmanContainer{}, errors.New("podman inspect returned empty container name")
	}
	return PodmanContainer{ID: id, Name: name, CgroupPath: strings.TrimSpace(rows[0].State.CgroupPath)}, nil
}

func (r CLIResolver) NamespaceForContainer(ctx context.Context, containerID string) (string, error) {
	containerID = strings.TrimSpace(containerID)
	if containerID == "" {
		return "", errors.New("container id is empty")
	}
	ns, criErr := r.namespaceFromCRICTL(ctx, containerID)
	if criErr == nil && ns != "" {
		return ns, nil
	}
	ns, kubectlErr := r.namespaceFromKubectl(ctx, containerID)
	if kubectlErr == nil && ns != "" {
		return ns, nil
	}
	return "", combineNamespaceErrors(criErr, kubectlErr)
}

func combineNamespaceErrors(criErr, kubectlErr error) error {
	criAbsent := errors.Is(criErr, ErrNotFound)
	kubectlAbsent := errors.Is(kubectlErr, ErrNotFound)
	criUnavailable := errors.Is(criErr, exec.ErrNotFound)
	kubectlUnavailable := errors.Is(kubectlErr, exec.ErrNotFound)
	if (criAbsent || criUnavailable) && (kubectlAbsent || kubectlUnavailable) && (criAbsent || kubectlAbsent) {
		return fmt.Errorf("%w: kubernetes container", ErrNotFound)
	}
	var infrastructureErrors []error
	for _, err := range []error{criErr, kubectlErr} {
		if err != nil && !errors.Is(err, ErrNotFound) {
			infrastructureErrors = append(infrastructureErrors, err)
		}
	}
	if len(infrastructureErrors) > 0 {
		return errors.Join(infrastructureErrors...)
	}
	return errors.New("kubernetes namespace resolution was inconclusive")
}

func (r CLIResolver) namespaceFromCRICTL(ctx context.Context, containerID string) (string, error) {
	ctx, cancel := r.withTimeout(ctx)
	defer cancel()
	out, err := limitedCommandOutput(ctx, "crictl", "inspect", containerID)
	if err != nil {
		if commandReportsNotFound(err) {
			return "", fmt.Errorf("%w: CRI container", ErrNotFound)
		}
		return "", err
	}
	var raw any
	if err := json.Unmarshal(out, &raw); err != nil {
		return "", err
	}
	ns := findNamespace(raw)
	if ns == "" {
		return "", errors.New("namespace not found in crictl inspect")
	}
	return ns, nil
}

func (r CLIResolver) namespaceFromKubectl(ctx context.Context, containerID string) (string, error) {
	ctx, cancel := r.withTimeout(ctx)
	defer cancel()
	out, err := limitedCommandOutput(ctx, "kubectl", "get", "pod", "-A", "-o", "json")
	if err != nil {
		return "", err
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Status struct {
				ContainerStatuses     []KubeContainerStatus `json:"containerStatuses"`
				InitContainerStatuses []KubeContainerStatus `json:"initContainerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return "", err
	}
	short := shortID(containerID)
	for _, item := range list.Items {
		if statusHasContainer(item.Status.ContainerStatuses, containerID, short) ||
			statusHasContainer(item.Status.InitContainerStatuses, containerID, short) {
			return item.Metadata.Namespace, nil
		}
	}
	return "", fmt.Errorf("%w: container namespace", ErrNotFound)
}

func (r CLIResolver) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return context.WithTimeout(ctx, timeout)
}

func limitedCommandOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	return limitedCommandOutputAs(ctx, 0, name, args...)
}

func limitedCommandOutputAs(ctx context.Context, uid int, name string, args ...string) ([]byte, error) {
	output := boundedOutput{limit: maxRuntimeOutputBytes}
	errorOutput := boundedOutput{limit: maxRuntimeErrorBytes}
	cmd := exec.CommandContext(ctx, name, args...)
	if err := configureCommandForUID(cmd, uid); err != nil {
		return nil, err
	}
	cmd.Stdout = &output
	cmd.Stderr = &errorOutput
	if err := cmd.Run(); err != nil {
		return nil, &commandError{name: name, err: err, stderr: errorOutput.String(), stderrExceeded: errorOutput.exceeded}
	}
	if output.exceeded {
		return nil, fmt.Errorf("%s output exceeds %d bytes", name, maxRuntimeOutputBytes)
	}
	return output.Bytes(), nil
}

func configureCommandForUID(cmd *exec.Cmd, uid int) error {
	if uid != 0 {
		identity, err := user.LookupId(strconv.Itoa(uid))
		if err != nil {
			return fmt.Errorf("lookup uid %d: %w", uid, err)
		}
		gid, err := strconv.ParseUint(identity.Gid, 10, 32)
		if err != nil {
			return fmt.Errorf("parse gid for uid %d: %w", uid, err)
		}
		groupIDs, err := identity.GroupIds()
		if err != nil {
			return fmt.Errorf("lookup groups for uid %d: %w", uid, err)
		}
		groups := make([]uint32, 0, len(groupIDs))
		for _, value := range groupIDs {
			group, err := strconv.ParseUint(value, 10, 32)
			if err != nil {
				return fmt.Errorf("parse group for uid %d: %w", uid, err)
			}
			groups = append(groups, uint32(group))
		}
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{
			Uid: uint32(uid), Gid: uint32(gid), Groups: groups,
		}}
		cmd.Env = []string{
			"HOME=" + identity.HomeDir,
			"USER=" + identity.Username,
			"LOGNAME=" + identity.Username,
			"XDG_RUNTIME_DIR=/run/user/" + strconv.Itoa(uid),
			"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		}
	} else {
		cmd.Env = os.Environ()
	}
	return nil
}

func commandReportsNotFound(err error) bool {
	var commandErr *commandError
	if !errors.As(err, &commandErr) || commandErr.stderrExceeded {
		return false
	}
	stderr := strings.ToLower(commandErr.stderr)
	switch commandErr.name {
	case "docker":
		return strings.Contains(stderr, "no such object") || strings.Contains(stderr, "no such container")
	case "podman":
		return strings.Contains(stderr, "no such container") ||
			strings.Contains(stderr, "no container with name or id") ||
			strings.Contains(stderr, "container does not exist")
	case "crictl":
		return strings.Contains(stderr, "code = notfound")
	default:
		return false
	}
}

func isHex(value string) bool {
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}

func findNamespace(value any) string {
	root, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	status, ok := root["status"].(map[string]any)
	if !ok {
		return ""
	}
	for _, field := range []string{"labels", "annotations"} {
		metadata, ok := status[field].(map[string]any)
		if !ok {
			continue
		}
		namespace, ok := metadata["io.kubernetes.pod.namespace"].(string)
		if ok && strings.TrimSpace(namespace) != "" {
			return strings.TrimSpace(namespace)
		}
	}
	return ""
}

type KubeContainerStatus struct {
	ContainerID string `json:"containerID"`
}

func statusHasContainer(statuses []KubeContainerStatus, full, short string) bool {
	for _, status := range statuses {
		id := extractID(status.ContainerID)
		if id == "" {
			continue
		}
		if id == full || id == short ||
			(len(id) >= 12 && strings.HasPrefix(full, id)) ||
			(len(short) >= 12 && strings.HasPrefix(id, short)) {
			return true
		}
	}
	return false
}

func extractID(value string) string {
	if i := strings.LastIndex(value, "://"); i >= 0 {
		value = value[i+3:]
	}
	return strings.ToLower(strings.TrimSpace(value))
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
