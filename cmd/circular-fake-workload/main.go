package main

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"

	"github.com/ruohao1/circular/internal/fakeworkload"
)

func main() {
	write := flag.Bool("write-output", false, "write deterministic Run-scoped output in the current worktree")
	probe := flag.Bool("probe-isolation", false, "report only safe isolation facts for the container test fixture")
	flag.Parse()
	if *probe {
		exists := func(path string) bool { _, err := os.Stat(path); return err == nil }
		files, _ := filepath.Glob("/workspace/circular-result-*")
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"uid": os.Getuid(), "database_url": os.Getenv("DATABASE_URL") != "", "docker_socket": exists("/var/run/docker.sock"), "ssh_directory": exists("/root/.ssh"), "output_files": files})
		return
	}
	os.Exit(fakeworkload.Run(context.Background(), os.Stdin, os.Stdout, os.Stderr, *write))
}
