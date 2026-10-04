package runtimes_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ruohao1/circular/internal/runtimes"
)

func TestCredentialMountIsExplicitAndChangesPolicyIdentity(t *testing.T) {
	base := t.TempDir()
	config := runtimes.DockerConfig{WorktreeRoot: filepath.Join(base, "worktrees")}
	plain, err := runtimes.NewDocker(config)
	if err != nil {
		t.Fatal(err)
	}
	spec := runtimes.Spec{RunID: runID, Worktree: filepath.Join(config.WorktreeRoot, runID.String()), Image: "fixture:test", CPULimit: 1, MemoryLimitMB: 256}
	legacy, err := plain.Resolve(spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.UseCredentials = true
	if _, err := plain.Resolve(spec); !errors.Is(err, runtimes.ErrInvalidSpec) {
		t.Fatalf("unconfigured credential mount was accepted: %v", err)
	}
	previous := legacy.PolicyDigest
	for _, name := range []string{"credentials", "other-credentials"} {
		// The source may exist only in the Docker daemon's namespace. Resolving
		// policy must not allocate directories or read credential data.
		config.CredentialRoot = filepath.Join(base, name)
		d, err := runtimes.NewDocker(config)
		if err != nil {
			t.Fatal(err)
		}
		spec.UseCredentials = false
		without, err := d.Resolve(spec)
		if err != nil || without.CredentialSource != "" || without.CredentialDestination != "" || without.PolicyDigest != legacy.PolicyDigest {
			t.Fatalf("configuring credentials changed a non-credential Run: %+v %v", without, err)
		}
		spec.UseCredentials = true
		with, err := d.Resolve(spec)
		if err != nil || with.CredentialSource != config.CredentialRoot || with.CredentialDestination != "/codex-auth" || !with.RootReadOnly || with.PolicyDigest == previous {
			t.Fatalf("credential source was not explicitly bound to policy: %+v %v", with, err)
		}
		previous = with.PolicyDigest
		if _, err := os.Stat(config.CredentialRoot); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("credential policy resolution allocated its source")
		}
	}
}

func TestCredentialRootsMustBeSeparateDirectories(t *testing.T) {
	base := t.TempDir()
	worktrees := filepath.Join(base, "worktrees")
	if err := os.Mkdir(worktrees, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(worktrees, link); err != nil {
		t.Fatal(err)
	}
	for name, root := range map[string]string{
		"relative": "relative-auth", "filesystem-root": "/", "unsafe-comma": filepath.Join(base, "auth,unsafe"),
		"invalid-nul": base + "\x00", "same": worktrees, "descendant": filepath.Join(worktrees, "auth"),
		"ancestor": base, "file": file, "symlink": link, "symlink-ancestor-overlap": filepath.Join(link, "auth"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := runtimes.NewDocker(runtimes.DockerConfig{WorktreeRoot: worktrees, CredentialRoot: root}); !errors.Is(err, runtimes.ErrInvalidConfiguration) {
				t.Fatalf("unsafe credential root accepted: %v", err)
			}
		})
	}
	if _, err := runtimes.NewDocker(runtimes.DockerConfig{WorktreeRoot: worktrees, CredentialRoot: worktrees + "-auth"}); err != nil {
		t.Fatalf("separate roots sharing a string prefix were rejected: %v", err)
	}
}

func TestCredentialRootReplacementIsRejectedBeforeLaunch(t *testing.T) {
	for _, ancestor := range []bool{false, true} {
		t.Run(map[bool]string{false: "directory", true: "ancestor"}[ancestor], func(t *testing.T) {
			base := t.TempDir()
			config := runtimes.DockerConfig{WorktreeRoot: filepath.Join(base, "worktrees"), CredentialRoot: filepath.Join(base, "parent", "auth")}
			if err := os.MkdirAll(config.CredentialRoot, 0700); err != nil {
				t.Fatal(err)
			}
			d, err := runtimes.NewDocker(config)
			if err != nil {
				t.Fatal(err)
			}
			replaced := config.CredentialRoot
			if ancestor {
				replaced = filepath.Dir(replaced)
			}
			if err := os.Rename(replaced, replaced+"-original"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(replaced+"-original", replaced); err != nil {
				t.Fatal(err)
			}
			spec := runtimes.Spec{RunID: runID, Worktree: filepath.Join(config.WorktreeRoot, runID.String()), Image: "fixture:test", CPULimit: 1, MemoryLimitMB: 256, UseCredentials: true}
			if _, err := d.Resolve(spec); !errors.Is(err, runtimes.ErrInvalidSpec) {
				t.Fatalf("replaced credential root accepted: %v", err)
			}
		})
	}
}

func TestCredentialMountOrderDoesNotChangeStartup(t *testing.T) {
	d, spec, state := simulatedDocker(t, map[string]any{"reverse_mounts": true}, func(config *runtimes.DockerConfig) {
		config.CredentialRoot = filepath.Join(filepath.Dir(config.WorktreeRoot), "credentials")
	})
	spec.UseCredentials = true
	handle, err := d.Start(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Discard(context.Background(), handle) })
	if operationCount(t, state, "start") != 1 {
		t.Fatal("credential container did not start")
	}
	spec.UseCredentials = false
	if _, err := d.Start(t.Context(), spec); !errors.Is(err, runtimes.ErrNameConflict) {
		t.Fatalf("changing credential access reused a live container: %v", err)
	}
}

func TestCredentialMountMismatchIsRemovedWithoutStarting(t *testing.T) {
	for _, mismatch := range []string{"credential_missing", "credential_type", "credential_source", "credential_destination", "credential_rw", "credential_duplicate", "workspace_missing", "credential_unrequested"} {
		t.Run(mismatch, func(t *testing.T) {
			d, spec, state := simulatedDocker(t, map[string]any{"policy_mismatch": mismatch}, func(config *runtimes.DockerConfig) {
				config.CredentialRoot = filepath.Join(filepath.Dir(config.WorktreeRoot), "credentials")
			})
			spec.UseCredentials = mismatch != "credential_unrequested"
			if _, err := d.Start(t.Context(), spec); !errors.Is(err, runtimes.ErrStart) {
				t.Fatalf("unsafe credential mount accepted: %v", err)
			}
			if exists(filepath.Join(state, "created")) || exists(filepath.Join(state, "start-invoked")) {
				t.Fatal("unsafe credential allocation remained or ran")
			}
		})
	}
}

func TestReleaseRequiresTheConfiguredCredentialSource(t *testing.T) {
	for _, test := range []struct {
		name, mismatch                        string
		configured, mounted, changed, allowed bool
	}{
		{name: "legacy-with-configuration", configured: true, allowed: true},
		{name: "credentials", configured: true, mounted: true, allowed: true},
		{name: "configuration-removed", mounted: true},
		{name: "configuration-changed", configured: true, mounted: true, changed: true},
		{name: "wrong-source", configured: true, mounted: true, mismatch: "credential_source"},
		{name: "wrong-destination", configured: true, mounted: true, mismatch: "credential_destination"},
		{name: "wrong-type", configured: true, mounted: true, mismatch: "credential_type"},
		{name: "read-only", configured: true, mounted: true, mismatch: "credential_rw"},
		{name: "duplicate", configured: true, mounted: true, mismatch: "credential_duplicate"},
		{name: "workspace-missing", configured: true, mounted: true, mismatch: "workspace_missing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var credentials string
			d, spec, state := simulatedDocker(t, map[string]any{"policy_mismatch": test.mismatch, "reverse_mounts": true}, func(config *runtimes.DockerConfig) {
				credentials = filepath.Join(filepath.Dir(config.WorktreeRoot), "credentials")
				if test.configured {
					config.CredentialRoot = credentials
					if test.changed {
						config.CredentialRoot += "-new"
					}
				}
			})
			plan, err := d.Resolve(spec)
			if err != nil {
				t.Fatal(err)
			}
			args := []string{"create", "--name", plan.ContainerName,
				"--label", "io.circular.managed=true", "--label", "io.circular.run_id=" + runID.String(),
				"--mount", "type=bind,src=" + spec.Worktree + ",dst=/workspace"}
			if test.mounted {
				args = append(args, "--mount", "type=bind,src="+credentials+",dst=/codex-auth")
			}
			args = append(args, spec.Image)
			create := exec.CommandContext(t.Context(), filepath.Join(filepath.Dir(state), "fake-docker"), args...)
			if output, err := create.CombinedOutput(); err != nil {
				t.Fatalf("seed abandoned allocation: %v %s", err, output)
			}
			err = d.Release(t.Context(), runID, "")
			if test.allowed {
				if err != nil || exists(filepath.Join(state, "created")) {
					t.Fatalf("owned credential allocation was not released: %v", err)
				}
			} else if !errors.Is(err, runtimes.ErrDiscard) || exists(filepath.Join(state, "rm-started")) {
				t.Fatalf("unexpected credential mount was not protected: %v", err)
			}
		})
	}
}
