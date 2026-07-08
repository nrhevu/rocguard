package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"rocguardd/internal/config"
	"rocguardd/internal/daemon"
	"rocguardd/internal/model"
	"rocguardd/internal/protocol"
	"rocguardd/internal/store"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "rocguard:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg := config.Default()
	if len(args) == 0 {
		usage()
		return nil
	}
	switch args[0] {
	case "daemon":
		return daemonCommand(cfg, args[1:])
	case "show-root-key", "show-key":
		return showRootKey(cfg)
	case "show-keys":
		rootKey, err := rootKeyFromEnvOrPrompt()
		if err != nil {
			return err
		}
		return printRPC(cfg, "show_keys", "", protocol.RootKeyArgs{RootKey: rootKey})
	case "register":
		return register(cfg, args[1:])
	case "run":
		return runCommand(cfg, args[1:])
	case "allow":
		return allowCommand(cfg, args[1:])
	case "docker":
		return dockerCommand(cfg, args[1:])
	case "k8s":
		return k8sCommand(cfg, args[1:])
	case "status":
		return printRPC(cfg, "status", tokenFromEnv(), nil)
	case "ps":
		return psCommand(cfg)
	case "who":
		return whoCommand(cfg, args[1:])
	case "token":
		return tokenCommand(cfg, args[1:])
	case "bypass":
		return bypassCommand(cfg, args[1:])
	case "revoke":
		if len(args) != 2 {
			return errors.New("usage: rocguard revoke <token-or-reservation-or-authorization-or-bypass-id>")
		}
		rootKey, err := rootKeyFromEnvOrPrompt()
		if err != nil {
			return err
		}
		return printRPC(cfg, "revoke", "", protocol.RevokeArgs{RootKey: rootKey, ID: args[1]})
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func daemonCommand(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dryRun := fs.Bool("dry-run", false, "record decisions without killing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dryRun {
		cfg.DryRun = true
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return daemon.New(cfg).Run(ctx)
}

func showRootKey(cfg config.Config) error {
	if os.Geteuid() != 0 {
		return errors.New("show-root-key requires uid 0")
	}
	key, err := store.New(cfg).ReadOrCreateRootKey()
	if err != nil {
		return err
	}
	fmt.Println(key)
	return nil
}

func register(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("register", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	reserved := fs.Bool("reserved", false, "create a reserved GPU key")
	claimed := fs.Bool("claimed", false, "create a claimed-mode key")
	hard := fs.Bool("hard", false, "legacy alias for --reserved")
	soft := fs.Bool("soft", false, "legacy alias for --claimed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	mode := ""
	for _, selected := range []struct {
		ok   bool
		mode string
	}{
		{*reserved, model.TokenModeReserved},
		{*claimed, model.TokenModeClaimed},
		{*hard, model.TokenModeReserved},
		{*soft, model.TokenModeClaimed},
	} {
		if !selected.ok {
			continue
		}
		if mode != "" && mode != selected.mode {
			return errors.New("usage: rocguard register (--reserved | --claimed)")
		}
		mode = selected.mode
	}
	if mode == "" {
		return errors.New("usage: rocguard register (--reserved | --claimed)")
	}
	reader := bufio.NewReader(os.Stdin)
	rootKey, err := prompt(reader, "Root key: ")
	if err != nil {
		return err
	}
	name, err := prompt(reader, "Name: ")
	if err != nil {
		return err
	}
	registerArgs := protocol.RegisterArgs{RootKey: rootKey, Name: name}
	if mode == model.TokenModeReserved {
		registerArgs.Mode = model.TokenModeReserved
		gpuText, err := prompt(reader, "GPU: ")
		if err != nil {
			return err
		}
		gpu, err := strconv.Atoi(strings.TrimSpace(gpuText))
		if err != nil || gpu < 0 {
			return errors.New("gpu must be >= 0")
		}
		ttl, err := prompt(reader, "TTL [2h]: ")
		if err != nil {
			return err
		}
		registerArgs.GPU = gpu
		registerArgs.TTL = ttl
	} else {
		registerArgs.Mode = model.TokenModeClaimed
	}
	raw, err := callRPC(cfg, "register", "", registerArgs, false)
	if err != nil {
		return err
	}
	var result model.RegisterResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	fmt.Printf("Token: %s\nMode: %s\n", result.Token, result.Mode)
	if result.ReservationID != "" {
		fmt.Printf("Reservation: %s\nGPU: %d\n", result.ReservationID, result.GPU)
	}
	if result.ExpiresAt != nil {
		fmt.Printf("Expires at: %s\n", result.ExpiresAt.Format(time.RFC3339))
	}
	return nil
}

func runCommand(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	gpu := fs.Int("gpu", -1, "GPU id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	command := fs.Args()
	if len(command) > 0 && command[0] == "--" {
		command = command[1:]
	}
	if len(command) == 0 {
		return errors.New("usage: KEY=... rocguard run [--gpu <id>] -- <command>")
	}
	var gpuArg *int
	if *gpu >= 0 {
		value := *gpu
		gpuArg = &value
	}
	workdir, _ := os.Getwd()
	raw, err := callRPC(cfg, "run", requiredToken(), protocol.RunArgs{
		GPU:     gpuArg,
		Command: command,
		Workdir: workdir,
		Env:     os.Environ(),
	}, true)
	if err != nil {
		return err
	}
	var result model.RunResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	if result.ExitCode != 0 {
		os.Exit(result.ExitCode)
	}
	return nil
}

func dockerCommand(cfg config.Config, args []string) error {
	if len(args) == 0 || args[0] != "allow" {
		return errors.New("usage: KEY=... rocguard docker allow [--gpu <id>] --container <name-or-id>")
	}
	fs := flag.NewFlagSet("docker allow", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	gpu := fs.Int("gpu", -1, "GPU id")
	container := fs.String("container", "", "container name or id")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	return printRPC(cfg, "docker_allow", requiredToken(), protocol.DockerAllowArgs{GPU: optionalGPU(*gpu), Container: *container})
}

func k8sCommand(cfg config.Config, args []string) error {
	if len(args) == 0 || args[0] != "allow" {
		return errors.New("usage: KEY=... rocguard k8s allow [--gpu <id>] --namespace <name>")
	}
	fs := flag.NewFlagSet("k8s allow", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	gpu := fs.Int("gpu", -1, "GPU id")
	namespace := fs.String("namespace", "", "namespace")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	return printRPC(cfg, "k8s_allow", requiredToken(), protocol.K8sAllowArgs{GPU: optionalGPU(*gpu), Namespace: *namespace})
}

func allowCommand(cfg config.Config, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: KEY=... rocguard allow (docker|k8s|user) ...")
	}
	switch args[0] {
	case "docker":
		fs := flag.NewFlagSet("allow docker", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		gpu := fs.Int("gpu", -1, "GPU id")
		container := fs.String("container", "", "container name or id")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return printRPC(cfg, "allow_docker", requiredToken(), protocol.DockerAllowArgs{GPU: optionalGPU(*gpu), Container: *container})
	case "k8s":
		fs := flag.NewFlagSet("allow k8s", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		gpu := fs.Int("gpu", -1, "GPU id")
		namespace := fs.String("namespace", "", "namespace")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return printRPC(cfg, "allow_k8s", requiredToken(), protocol.K8sAllowArgs{GPU: optionalGPU(*gpu), Namespace: *namespace})
	case "user":
		fs := flag.NewFlagSet("allow user", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		gpu := fs.Int("gpu", -1, "GPU id")
		username := fs.String("user", "", "username")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return printRPC(cfg, "allow_user", requiredToken(), protocol.UserAllowArgs{GPU: optionalGPU(*gpu), User: *username})
	default:
		return fmt.Errorf("unknown allow scope %q", args[0])
	}
}

func whoCommand(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("who", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	gpu := fs.Int("gpu", -1, "GPU id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *gpu < 0 {
		return errors.New("usage: rocguard who --gpu <id>")
	}
	return printRPC(cfg, "who", tokenFromEnv(), protocol.WhoArgs{GPU: *gpu})
}

func tokenCommand(cfg config.Config, args []string) error {
	if len(args) != 1 || args[0] != "info" {
		return errors.New("usage: rocguard token info")
	}
	return printRPC(cfg, "token_info", requiredToken(), protocol.TokenInfoArgs{})
}

func bypassCommand(cfg config.Config, args []string) error {
	if len(args) == 0 || args[0] != "add" {
		return errors.New("usage: rocguard bypass add (--pid <pid> | --command <path> --uid <uid>) --ttl <duration> --reason <text>")
	}
	fs := flag.NewFlagSet("bypass add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	pid := fs.Int("pid", 0, "pid")
	command := fs.String("command", "", "absolute command path")
	uid := fs.Int("uid", 0, "uid")
	ttl := fs.String("ttl", "2h", "ttl")
	reason := fs.String("reason", "", "reason")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	kind := model.BypassPID
	if *command != "" {
		kind = model.BypassCommand
	}
	rootKey, err := rootKeyFromEnvOrPrompt()
	if err != nil {
		return err
	}
	return printRPC(cfg, "bypass_add", "", protocol.BypassAddArgs{
		RootKey: rootKey,
		Type:    kind,
		PID:     *pid,
		Command: *command,
		UID:     *uid,
		TTL:     *ttl,
		Reason:  *reason,
	})
}

func psCommand(cfg config.Config) error {
	raw, err := callRPC(cfg, "ps", tokenFromEnv(), nil, false)
	if err != nil {
		return err
	}
	var rows []model.PSRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return err
	}
	return writePSRows(os.Stdout, rows)
}

func writePSRows(out io.Writer, rows []model.PSRow) error {
	writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "id\tgpu\tuser\tcommand")
	for _, row := range rows {
		fmt.Fprintf(writer, "%s\t%d\t%s\t%s\n", row.ID, row.GPU, row.User, row.Command)
	}
	return writer.Flush()
}

func printRPC(cfg config.Config, method, token string, args any) error {
	raw, err := callRPC(cfg, method, token, args, false)
	if err != nil {
		return err
	}
	var pretty any
	if err := json.Unmarshal(raw, &pretty); err != nil {
		fmt.Println(string(raw))
		return nil
	}
	out, _ := json.MarshalIndent(pretty, "", "  ")
	fmt.Println(string(out))
	return nil
}

func rootKeyFromEnvOrPrompt() (string, error) {
	if value := strings.TrimSpace(os.Getenv("ROOT_KEY")); value != "" {
		return value, nil
	}
	return prompt(bufio.NewReader(os.Stdin), "Root key: ")
}

func optionalGPU(gpu int) *int {
	if gpu < 0 {
		return nil
	}
	return &gpu
}

func callRPC(cfg config.Config, method, token string, args any, stream bool) (json.RawMessage, error) {
	conn, err := net.Dial("unix", cfg.SocketPath)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", cfg.SocketPath, err)
	}
	defer conn.Close()
	var rawArgs json.RawMessage
	if args != nil {
		rawArgs, err = json.Marshal(args)
		if err != nil {
			return nil, err
		}
	}
	req := protocol.Request{
		ID:     strconv.FormatInt(time.Now().UnixNano(), 36),
		Method: method,
		Token:  token,
		Args:   rawArgs,
	}
	data, _ := json.Marshal(req)
	if _, err := conn.Write(append(data, '\n')); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(conn)
	for {
		var resp protocol.Response
		if err := decoder.Decode(&resp); err != nil {
			return nil, err
		}
		if resp.ID != req.ID {
			continue
		}
		switch resp.Kind {
		case protocol.KindStdout:
			if stream {
				fmt.Fprint(os.Stdout, resp.Data)
			}
		case protocol.KindStderr:
			if stream {
				fmt.Fprint(os.Stderr, resp.Data)
			}
		default:
			if !resp.OK {
				return nil, errors.New(resp.Error)
			}
			return resp.Result, nil
		}
	}
}

func prompt(reader *bufio.Reader, label string) (string, error) {
	fmt.Print(label)
	value, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(value), nil
}

func requiredToken() string {
	token := tokenFromEnv()
	if token == "" {
		fmt.Fprintln(os.Stderr, "rocguard: KEY token is required")
		os.Exit(1)
	}
	return token
}

func tokenFromEnv() string {
	return os.Getenv("KEY")
}

func usage() {
	fmt.Print(`rocguard commands:
  rocguard daemon [--dry-run]
  sudo rocguard show-root-key
  rocguard show-keys
  rocguard register (--reserved | --claimed)
  KEY=... rocguard run [--gpu <id>] -- <command>
  KEY=... rocguard allow docker [--gpu <id>] --container <name-or-id>
  KEY=... rocguard allow k8s [--gpu <id>] --namespace <name>
  KEY=... rocguard allow user [--gpu <id>] --user <name>
  KEY=... rocguard docker allow [--gpu <id>] --container <name-or-id>
  KEY=... rocguard k8s allow [--gpu <id>] --namespace <name>
  rocguard status
  rocguard ps
  rocguard who --gpu <id>
  rocguard token info
  rocguard bypass add (--pid <pid> | --command <path> --uid <uid>) --ttl <duration> --reason <text>
  rocguard revoke <token-or-reservation-or-authorization-or-bypass-id>
`)
}
