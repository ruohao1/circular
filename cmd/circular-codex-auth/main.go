package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/joho/godotenv"
	"github.com/ruohao1/circular/internal/codexlogin"
	"github.com/ruohao1/circular/internal/execution"
	"github.com/ruohao1/circular/internal/runtimes"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || args[0] != "login" && args[0] != "status" && args[0] != "logout" {
		_, _ = fmt.Fprintln(stderr, "usage: circular-codex-auth login|status|logout")
		return 2
	}
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		_, _ = fmt.Fprintln(stderr, "cannot read worker .env configuration")
		return 1
	}
	config, err := execution.LoadConfig(os.Getenv)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "invalid Codex authentication configuration")
		return 1
	}
	if config.CodexAuthMode != "chatgpt" {
		_, _ = fmt.Fprintln(stderr, "Codex authentication commands require CIRCULAR_CODEX_AUTH_MODE=chatgpt")
		return 1
	}
	if config.CodexAPIKey != "" {
		_, _ = fmt.Fprintln(stderr, "ChatGPT authentication cannot be combined with CIRCULAR_CODEX_API_KEY")
		return 1
	}
	if _, err := runtimes.NewDocker(config.Docker); err != nil {
		_, _ = fmt.Fprintln(stderr, "invalid Codex authentication Docker configuration")
		return 1
	}
	if err := execution.PrepareCodexAuth(config); err != nil {
		_, _ = fmt.Fprintln(stderr, "cannot prepare the private Codex authentication directory")
		return 1
	}
	return codexlogin.Run(ctx, codexlogin.Config{
		DockerExecutable: config.Docker.DockerExecutable,
		Image:            config.CodexImage, CredentialRoot: config.Docker.CredentialRoot,
		ContainerUser: config.Docker.ContainerUser,
	}, args[0], stdout, stderr)
}
