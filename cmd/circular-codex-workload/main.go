package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/ruohao1/circular/internal/agenttools"
	"github.com/ruohao1/circular/internal/codexworkload"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := 2
	if len(os.Args) == 3 && os.Args[1] == "mcp" {
		code = 0
		if err := agenttools.Run(ctx, os.Args[2]); err != nil {
			_, _ = os.Stderr.WriteString("Circular agent tools stopped\n")
			code = 1
		}
	} else if len(os.Args) == 4 && os.Args[1] == "mcp-review" {
		code = 0
		if err := agenttools.RunReview(ctx, os.Args[2], os.Args[3]); err != nil {
			_, _ = os.Stderr.WriteString("Circular review tools stopped\n")
			code = 1
		}
	} else if len(os.Args) == 1 {
		code = codexworkload.Run(ctx, os.Stdin, os.Stdout, os.Stderr)
	} else if len(os.Args) == 2 {
		code = codexworkload.Authenticate(ctx, os.Args[1], os.Stdin, os.Stdout, os.Stderr)
	} else {
		_, _ = os.Stderr.WriteString("expected login, status, logout, or no arguments\n")
	}
	stop()
	os.Exit(code)
}
