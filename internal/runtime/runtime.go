package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type Resolver interface {
	ResolveDockerContainer(ctx context.Context, nameOrID string) (string, error)
	DockerContainerName(ctx context.Context, containerID string) (string, error)
	NamespaceForContainer(ctx context.Context, containerID string) (string, error)
}

var ErrNotFound = errors.New("runtime object not found")

type CLIResolver struct {
	Timeout time.Duration
}

type cacheEntry struct {
	value     string
	err       error
	expiresAt time.Time
}

type resolveCall struct {
	done  chan struct{}
	value string
	err   error
}

type CachedResolver struct {
	Base Resolver
	TTL  time.Duration

	mu              sync.Mutex
	dockerIDs       map[string]cacheEntry
	dockerNames     map[string]cacheEntry
	namespaces      map[string]cacheEntry
	dockerIDCalls   map[string]*resolveCall
	dockerNameCalls map[string]*resolveCall
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
		dockerIDs: make(map[string]cacheEntry), dockerNames: make(map[string]cacheEntry), namespaces: make(map[string]cacheEntry),
		dockerIDCalls: make(map[string]*resolveCall), dockerNameCalls: make(map[string]*resolveCall), namespaceCalls: make(map[string]*resolveCall),
		now: time.Now,
	}
}

func (r *CachedResolver) ResolveDockerContainer(ctx context.Context, nameOrID string) (string, error) {
	key := strings.TrimSpace(nameOrID)
	return r.resolve(ctx, r.dockerIDs, r.dockerIDCalls, key, func() (string, error) {
		return r.Base.ResolveDockerContainer(ctx, nameOrID)
	})
}

func (r *CachedResolver) DockerContainerName(ctx context.Context, containerID string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(containerID))
	return r.resolve(ctx, r.dockerNames, r.dockerNameCalls, key, func() (string, error) {
		return r.Base.DockerContainerName(ctx, containerID)
	})
}

func (r *CachedResolver) NamespaceForContainer(ctx context.Context, containerID string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(containerID))
	return r.resolve(ctx, r.namespaces, r.namespaceCalls, key, func() (string, error) {
		return r.Base.NamespaceForContainer(ctx, containerID)
	})
}

func (r *CachedResolver) resolve(ctx context.Context, cache map[string]cacheEntry, calls map[string]*resolveCall, key string, call func() (string, error)) (string, error) {
	if r == nil || r.Base == nil {
		return "", errors.New("runtime resolver is unavailable")
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
			return "", ctx.Err()
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
	output := boundedOutput{limit: maxRuntimeOutputBytes}
	errorOutput := boundedOutput{limit: maxRuntimeErrorBytes}
	cmd := exec.CommandContext(ctx, name, args...)
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

func commandReportsNotFound(err error) bool {
	var commandErr *commandError
	if !errors.As(err, &commandErr) || commandErr.stderrExceeded {
		return false
	}
	stderr := strings.ToLower(commandErr.stderr)
	switch commandErr.name {
	case "docker":
		return strings.Contains(stderr, "no such object") || strings.Contains(stderr, "no such container")
	case "crictl":
		return strings.Contains(stderr, "code = notfound")
	default:
		return false
	}
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
