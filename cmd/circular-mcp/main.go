// circular-mcp is a local stdio MCP server for external coding agents.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ruohao1/circular/internal/controlmcp"
)

func run(ctx context.Context, args []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("circular-mcp", flag.ContinueOnError)
	flags.SetOutput(stderr)
	api := flags.String("api-url", controlmcp.DefaultAPIURL, "Circular API origin (optionally ending in /api/v1)")
	web := flags.String("web-url", controlmcp.DefaultWebURL, "Circular console origin for Run links")
	readOnly := flags.Bool("read-only", false, "expose only inspection tools; omit all mutation tools")
	check := flags.Bool("check", false, "check API connectivity and launch compatibility, then exit without starting MCP")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments; use --help for options")
	}
	config := controlmcp.Config{APIURL: *api, WebURL: *web, ReadOnly: *readOnly}
	if *check {
		if err := controlmcp.Check(ctx, config); err != nil {
			return err
		}
		_, err := fmt.Fprintln(stderr, "Circular API connected; MCP launch compatibility verified.")
		return err
	}
	server, err := controlmcp.New(config)
	if err != nil {
		return err
	}
	return server.Run(ctx, &mcp.StdioTransport{})
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stderr); err != nil && !errors.Is(err, flag.ErrHelp) && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "circular-mcp:", err)
		os.Exit(1)
	}
}
