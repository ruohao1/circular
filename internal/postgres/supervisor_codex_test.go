package postgres_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ruohao1/circular/internal/artifacts"
	"github.com/ruohao1/circular/internal/execution"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/testsupport"
)

const codexTestKey = "test-codex-key-never-persist"
const codexMessage = `{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"Implemented fixture 🌍"}}` + "\n"
const codexUsage = `{"type":"turn.completed","usage":{"input_tokens":21,"cached_input_tokens":8,"output_tokens":5}}` + "\n"
const codexSubscriptionMarker = "synthetic-subscription-auth-state-must-survive-cleanup"

func newCodexSupervisorFixture(t *testing.T, chunks []protocolChunk, exitCode int) supervisorFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codex-output.json")
	if chunks == nil {
		chunks = []protocolChunk{}
	}
	data, err := json.Marshal(protocolScript{Chunks: chunks, ExitCode: exitCode})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	f := newSupervisorFixture(t, map[string]any{"output_script": path})
	f.config.CodexEnabled = true
	f.config.CodexAuthMode = "api_key"
	f.config.CodexImage = "circular-codex-runner:test"
	f.config.CodexAPIKey = codexTestKey
	if _, err := f.pool.Exec(t.Context(), `UPDATE agents SET backend='codex',backend_config='{"model":"gpt-fixture-codex"}' WHERE id=(SELECT agent_id FROM runs WHERE id=$1)`, f.id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE runs SET backend='codex' WHERE id=$1`, f.id); err != nil {
		t.Fatal(err)
	}
	return f
}

func assertCodexResourcesReleased(t *testing.T, f supervisorFixture, snapshot testsupport.Snapshot) {
	t.Helper()
	if snapshot.Run.WorkerID != nil || snapshot.Workspace == nil || snapshot.Workspace.Status != "released" || snapshot.Workspace.ContainerID == nil {
		t.Fatalf("Codex resources were not released: %+v", snapshot)
	}
	for _, path := range []string{filepath.Join(f.config.Git.WorktreeRoot, f.id.String()), filepath.Join(f.base, "docker", "fake-docker-state", "created")} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("Codex allocation survived cleanup: %s: %v", path, err)
		}
	}
	snapshot.AssertReplay(t)
}

func TestSupervisorCodexCompletesAndRetainsNormalizedReplayAndArtifacts(t *testing.T) {
	// Split the message across chunks and mix diagnostic stderr with JSONL stdout.
	f := newCodexSupervisorFixture(t, []protocolChunk{
		{"stdout", []byte("{\"type\":\"thread.started\",\"thread_id\":\"fixture\"}\n{\"type\":\"turn.started\"}\n")},
		{"stderr", []byte("fixture diagnostic " + codexTestKey)},
		{"stdout", []byte(codexMessage[:len(codexMessage)-7])},
		{"stdout", []byte(codexMessage[len(codexMessage)-7:] + codexUsage)},
	}, 0)
	claim := acquire(t, postgres.NewQueue(f.pool), "codex-owner")
	supervisor, err := execution.NewSupervisor(f.pool, "codex-owner", f.config)
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Execute(t.Context(), *claim, "codex-owner"); err != nil {
		t.Fatal(err)
	}
	snapshot := testsupport.Observe(t, f.pool, f.id)
	if snapshot.Run.Status != "succeeded" || snapshot.Run.Error != nil {
		t.Fatalf("Codex success: %+v", snapshot)
	}
	assertCodexResourcesReleased(t, f, snapshot)
	snapshot.AssertTypes(t, "workspace.provisioning", "workspace.provisioning", "workspace.ready", "run.started", "agent.message.completed", "usage.updated", "artifact.created", "git.diff.updated", "run.completed", "artifact.created", "workspace.released")
	message, usage := snapshot.Events[4], snapshot.Events[5]
	if message.Source != "codex" || usage.Source != "codex" || message.Raw["type"] != "item.completed" || usage.Raw["type"] != "turn.completed" {
		t.Fatalf("Codex raw event identity was lost: %+v %+v", message, usage)
	}
	testsupport.AssertJSON(t, message.Data, map[string]any{"content": "Implemented fixture 🌍"})
	testsupport.AssertJSON(t, usage.Data, map[string]any{"input_tokens": 21, "output_tokens": 5})
	testsupport.AssertJSON(t, message.Raw["item"], map[string]any{"id": "item_1", "type": "agent_message", "text": "Implemented fixture 🌍"})
	testsupport.AssertJSON(t, usage.Raw["usage"], map[string]any{"input_tokens": 21, "cached_input_tokens": 8, "output_tokens": 5})
	if snapshot.Usage.InputTokens != "21" || snapshot.Usage.OutputTokens != "5" {
		t.Fatal("execution snapshot lost Codex usage")
	}
	store, err := artifacts.NewLocalStore(f.config.ArtifactRoot)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, artifact := range snapshot.Artifacts {
		kinds[artifact.Kind] = true
		data, err := store.Read(t.Context(), f.id, artifact.URI)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(codexTestKey)) {
			t.Fatal("worker credential reached an artifact")
		}
	}
	if len(snapshot.Artifacts) != 2 || !kinds["diff"] || !kinds["workspace"] {
		t.Fatalf("Codex artifacts were not retained: %+v", snapshot.Artifacts)
	}
	assertCodexCredentialAbsent(t, f, snapshot)
}

func TestSupervisorCodexFailuresRetainProgressAndCleanResources(t *testing.T) {
	failed := `{"type":"turn.failed","error":{"message":"fixture ` + codexTestKey + `"}}` + "\n"
	for _, test := range []struct {
		name, output, failure string
		exitCode              int
		messages              int
	}{
		{"turn_failed", codexMessage + failed, "Codex backend reported turn.failed", 0, 1},
		{"missing_completion", codexMessage, "Codex backend ended without a completed turn", 0, 1},
		{"nonzero_exit", codexMessage + codexUsage, "Codex backend exited with code 23", 23, 1},
		{"malformed_json", "not-json\n", "Codex backend emitted invalid JSON at stdout line 1", 0, 0},
		{"progress_then_malformed", codexMessage + "not-json\n", "Codex backend emitted invalid JSON at stdout line 2", 0, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newCodexSupervisorFixture(t, []protocolChunk{{"stdout", []byte(test.output)}}, test.exitCode)
			claim := acquire(t, postgres.NewQueue(f.pool), "codex-owner")
			supervisor, err := execution.NewSupervisor(f.pool, "codex-owner", f.config)
			if err != nil {
				t.Fatal(err)
			}
			if err := supervisor.Execute(t.Context(), *claim, "codex-owner"); err == nil || err.Error() != test.failure {
				t.Fatalf("Codex failure: %v; expected %s", err, test.failure)
			}
			snapshot := testsupport.Observe(t, f.pool, f.id)
			if snapshot.Run.Status != "failed" || snapshot.Run.Error == nil || *snapshot.Run.Error != test.failure || snapshot.Count("run.failed") != 1 || snapshot.Count("run.completed") != 0 || snapshot.Count("agent.message.completed") != test.messages {
				t.Fatalf("Codex failure projection: %+v", snapshot)
			}
			assertCodexResourcesReleased(t, f, snapshot)
			if len(snapshot.Artifacts) != 2 {
				t.Fatalf("failed Codex output was not retained: %+v", snapshot.Artifacts)
			}
			for _, event := range snapshot.Events {
				if event.Type == "run.failed" && test.name == "turn_failed" {
					testsupport.AssertJSON(t, event.Raw, map[string]any{"type": "turn.failed", "error": map[string]any{"message": "fixture [REDACTED]"}})
				}
			}
			assertCodexCredentialAbsent(t, f, snapshot)
		})
	}
}

func assertCodexCredentialAbsent(t *testing.T, f supervisorFixture, snapshot testsupport.Snapshot) {
	t.Helper()
	data, err := json.Marshal(snapshot.Events)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(codexTestKey)) {
		t.Fatal("worker credential reached persisted events")
	}
	var config string
	if err := f.pool.QueryRow(t.Context(), `SELECT backend_config::text FROM agents WHERE id=(SELECT agent_id FROM runs WHERE id=$1)`, f.id).Scan(&config); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(config, codexTestKey) {
		t.Fatal("worker credential reached Agent configuration")
	}
}

func TestSupervisorCodexDisabledWorkerFailsBeforeAllocating(t *testing.T) {
	f := newCodexSupervisorFixture(t, nil, 0)
	f.config.CodexEnabled = false
	claim := acquire(t, postgres.NewQueue(f.pool), "codex-owner")
	supervisor, err := execution.NewSupervisor(f.pool, "codex-owner", f.config)
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Execute(t.Context(), *claim, "codex-owner"); err == nil {
		t.Fatal("disabled Codex backend executed")
	}
	snapshot := testsupport.Observe(t, f.pool, f.id)
	snapshot.AssertReplay(t)
	if snapshot.Run.Status != "failed" || snapshot.Run.WorkerID != nil || snapshot.Workspace != nil || len(snapshot.Artifacts) != 0 || snapshot.Count("run.failed") != 1 || snapshot.Count("run.started") != 0 {
		t.Fatalf("disabled backend allocated execution resources: %+v", snapshot)
	}
	if snapshot.Run.Error == nil || *snapshot.Run.Error != "Codex backend is disabled on this worker" {
		t.Fatalf("disabled backend reason was lost: %v", snapshot.Run.Error)
	}
	for _, path := range []string{filepath.Join(f.config.Git.WorktreeRoot, f.id.String()), filepath.Join(f.base, "docker", "fake-docker-state", "create-started")} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("disabled backend touched an allocation: %s: %v", path, err)
		}
	}
}

func TestSupervisorCodexAPICancellationStopsStreamingAndCleansResources(t *testing.T) {
	chunks := []protocolChunk{{"stdout", []byte(codexMessage)}}
	for range 200 {
		chunks = append(chunks, protocolChunk{"stdout", []byte("{\"type\":\"item.updated\",\"item\":{\"id\":\"item_2\",\"type\":\"command_execution\"}}\n")})
	}
	chunks = append(chunks, protocolChunk{"stdout", []byte(codexUsage)})
	f := newCodexSupervisorFixture(t, chunks, 0)
	claim := acquire(t, postgres.NewQueue(f.pool), "codex-owner")
	attempt := launchSupervisor(t, f, *claim, "codex-owner")
	deadline := time.Now().Add(5 * time.Second)
	for {
		var count int
		if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE run_id=$1 AND source='codex' AND type='agent.message.completed'`, f.id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Codex progress was not visible before completion")
		}
		time.Sleep(10 * time.Millisecond)
	}
	testsupport.Cancel(t, f.pool, f.id)
	if err := attempt.wait(t); err != nil {
		t.Fatalf("Codex cancellation did not settle: %v", err)
	}
	snapshot := testsupport.Observe(t, f.pool, f.id)
	if snapshot.Run.Status != "cancelled" || snapshot.Run.Error != nil || snapshot.Count("run.cancelled") != 1 || snapshot.Count("run.failed") != 0 || snapshot.Count("run.completed") != 0 || snapshot.Count("agent.message.completed") != 1 {
		t.Fatalf("Codex cancellation: %+v", snapshot)
	}
	assertCodexResourcesReleased(t, f, snapshot)
	cancelled := false
	for _, event := range snapshot.Events {
		if cancelled && event.Source == "codex" {
			t.Fatal("Codex events were appended after cancellation")
		}
		cancelled = cancelled || event.Type == "run.cancelled"
	}
	if len(snapshot.Artifacts) != 2 {
		t.Fatal("cancelled Codex output was not retained")
	}
}

func newCodexSubscriptionFixture(t *testing.T, chunks []protocolChunk, exitCode int) supervisorFixture {
	t.Helper()
	f := newCodexSupervisorFixture(t, chunks, exitCode)
	f.config.CodexAuthMode, f.config.CodexAPIKey = "chatgpt", ""
	f.config.CodexAuthRoot = filepath.Join(f.base, "codex-auth")
	f.config.Docker.CredentialRoot = f.config.CodexAuthRoot
	if err := os.Mkdir(f.config.CodexAuthRoot, 0700); err != nil {
		t.Fatal(err)
	}
	// The scripted process substitutes only wrapper output. No login is read;
	// this marker models the separate persistent subscription state.
	if err := os.WriteFile(filepath.Join(f.config.CodexAuthRoot, "auth.marker"), []byte(codexSubscriptionMarker), 0600); err != nil {
		t.Fatal(err)
	}
	return f
}

func assertCodexSubscriptionBoundary(t *testing.T, f supervisorFixture, snapshot testsupport.Snapshot) {
	t.Helper()
	marker, err := os.ReadFile(filepath.Join(f.config.CodexAuthRoot, "auth.marker"))
	if err != nil || string(marker) != codexSubscriptionMarker {
		t.Fatalf("Run cleanup changed the dedicated subscription state: %v", err)
	}
	info, err := os.Lstat(f.config.CodexAuthRoot)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatalf("Run cleanup changed the private auth directory: %v", err)
	}
	state := filepath.Join(f.base, "docker", "fake-docker-state")
	input, err := os.ReadFile(filepath.Join(state, "stdin.bin"))
	if err != nil {
		t.Fatal(err)
	}
	var request map[string]any
	if err := json.Unmarshal(input, &request); err != nil {
		t.Fatal(err)
	}
	if request["auth_mode"] != "chatgpt" || len(request) != 4 || request["model"] != "gpt-fixture-codex" {
		t.Fatalf("subscription invocation changed: %+v", request)
	}
	if _, exists := request["api_key"]; exists {
		t.Fatal("subscription invocation carried an API credential")
	}
	for _, filename := range []string{"stdin.bin", "create-argv.json", "create-environment.json"} {
		data, err := os.ReadFile(filepath.Join(state, filename))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(codexTestKey)) || bytes.Contains(data, []byte(codexSubscriptionMarker)) {
			t.Fatalf("subscription state reached Docker input or process metadata: %s", filename)
		}
	}
	data, err := os.ReadFile(filepath.Join(state, "create-environment.json"))
	if err != nil {
		t.Fatal(err)
	}
	var environment map[string]string
	if err := json.Unmarshal(data, &environment); err != nil {
		t.Fatal(err)
	}
	if environment["PATH"] != "/bin:/usr/bin" {
		t.Fatal("subscription credentials were added to Docker's process environment")
	}
	for name := range environment {
		// The simulator's /bin/sh launcher adds PWD before invoking the fixture.
		if name != "PATH" && name != "PWD" {
			t.Fatalf("unexpected subscription Docker environment name: %s", name)
		}
	}
	data, err = os.ReadFile(filepath.Join(state, "create-argv.json"))
	if err != nil {
		t.Fatal(err)
	}
	var arguments []string
	if err := json.Unmarshal(data, &arguments); err != nil {
		t.Fatal(err)
	}
	mounts := map[string]int{}
	for index, argument := range arguments {
		if argument == "--mount" && index+1 < len(arguments) {
			mounts[arguments[index+1]]++
		}
	}
	worktree := "type=bind,src=" + filepath.Join(f.config.Docker.WorktreeRoot, f.id.String()) + ",dst=/workspace"
	auth := "type=bind,src=" + f.config.Docker.CredentialRoot + ",dst=/codex-auth"
	if len(mounts) != 2 || mounts[worktree] != 1 || mounts[auth] != 1 {
		t.Fatalf("subscription launch did not separate auth from the worktree: %+v", mounts)
	}
	store, err := artifacts.NewLocalStore(f.config.ArtifactRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Artifacts) != 2 {
		t.Fatalf("subscription Run did not retain its diff and workspace: %+v", snapshot.Artifacts)
	}
	for _, artifact := range snapshot.Artifacts {
		data, err := store.Read(t.Context(), f.id, artifact.URI)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(codexSubscriptionMarker)) || bytes.Contains(data, []byte("auth.marker")) || bytes.Contains(data, []byte(codexTestKey)) {
			t.Fatal("dedicated subscription state reached a retained artifact")
		}
	}
	data, err = json.Marshal(snapshot.Events)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(codexSubscriptionMarker)) {
		t.Fatal("dedicated subscription state reached persisted replay")
	}
	assertCodexCredentialAbsent(t, f, snapshot)
}

func TestSupervisorCodexSubscriptionLifecyclePreservesDedicatedAuth(t *testing.T) {
	for _, cancelRun := range []bool{false, true} {
		name := "success"
		if cancelRun {
			name = "cancel"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("OPENAI_API_KEY", codexTestKey)
			chunks := []protocolChunk{{"stdout", []byte(codexMessage)}}
			if cancelRun {
				for range 200 {
					chunks = append(chunks, protocolChunk{"stdout", []byte("{\"type\":\"item.updated\",\"item\":{\"id\":\"pending\",\"type\":\"command_execution\"}}\n")})
				}
			}
			chunks = append(chunks, protocolChunk{"stdout", []byte(codexUsage)})
			f := newCodexSubscriptionFixture(t, chunks, 0)
			claim := acquire(t, postgres.NewQueue(f.pool), "subscription-owner")
			attempt := launchSupervisor(t, f, *claim, "subscription-owner")
			if cancelRun {
				deadline := time.Now().Add(5 * time.Second)
				for {
					var count int
					if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE run_id=$1 AND source='codex' AND type='agent.message.completed'`, f.id).Scan(&count); err != nil {
						t.Fatal(err)
					}
					if count == 1 {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("subscription progress did not stream before completion")
					}
					time.Sleep(10 * time.Millisecond)
				}
				if _, err := os.Stat(filepath.Join(f.config.Git.WorktreeRoot, f.id.String(), "auth.marker")); !os.IsNotExist(err) {
					t.Fatal("subscription state entered the live Run worktree")
				}
				testsupport.Cancel(t, f.pool, f.id)
			}
			if err := attempt.wait(t); err != nil {
				t.Fatalf("subscription lifecycle did not settle: %v", err)
			}
			snapshot := testsupport.Observe(t, f.pool, f.id)
			expected := "succeeded"
			if cancelRun {
				expected = "cancelled"
			}
			if snapshot.Run.Status != expected || snapshot.Run.Error != nil || snapshot.Count("agent.message.completed") != 1 || snapshot.Count("run.failed") != 0 {
				t.Fatalf("subscription lifecycle projection: %+v", snapshot)
			}
			if cancelRun {
				if snapshot.Count("run.cancelled") != 1 || snapshot.Count("run.completed") != 0 || snapshot.Count("usage.updated") != 0 {
					t.Fatalf("cancelled subscription Run completed: %+v", snapshot)
				}
			} else if snapshot.Count("run.completed") != 1 || snapshot.Count("usage.updated") != 1 || snapshot.Usage.InputTokens != "21" || snapshot.Usage.OutputTokens != "5" {
				t.Fatalf("subscription completion lost normalized usage: %+v", snapshot)
			}
			cancelled := false
			for _, event := range snapshot.Events {
				if cancelled && event.Source == "codex" {
					t.Fatal("subscription events were persisted after cancellation")
				}
				if event.Type == "agent.message.completed" {
					if event.Source != "codex" || event.Raw["type"] != "item.completed" {
						t.Fatal("subscription message lost its backend identity")
					}
					testsupport.AssertJSON(t, event.Data, map[string]any{"content": "Implemented fixture 🌍"})
				}
				cancelled = cancelled || event.Type == "run.cancelled"
			}
			assertCodexResourcesReleased(t, f, snapshot)
			assertCodexSubscriptionBoundary(t, f, snapshot)
		})
	}
}

func TestSupervisorCodexSubscriptionMissingLoginRetainsPublicFailure(t *testing.T) {
	const message = "Codex ChatGPT login is unavailable; run the Circular Codex login command"
	raw := map[string]any{"type": "error", "code": "circular_chatgpt_auth_required", "message": message}
	output, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	f := newCodexSubscriptionFixture(t, []protocolChunk{{"stdout", append(output, '\n')}}, 1)
	claim := acquire(t, postgres.NewQueue(f.pool), "subscription-owner")
	supervisor, err := execution.NewSupervisor(f.pool, "subscription-owner", f.config)
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Execute(t.Context(), *claim, "subscription-owner"); err == nil || err.Error() != message {
		t.Fatalf("missing-login guidance was not propagated: %v", err)
	}
	snapshot := testsupport.Observe(t, f.pool, f.id)
	if snapshot.Run.Status != "failed" || snapshot.Run.Error == nil || *snapshot.Run.Error != message || snapshot.Count("run.failed") != 1 || snapshot.Count("run.completed") != 0 || snapshot.Count("agent.message.completed") != 0 {
		t.Fatalf("missing-login failure projection: %+v", snapshot)
	}
	for _, event := range snapshot.Events {
		if event.Type == "run.failed" {
			testsupport.AssertJSON(t, event.Raw, raw)
		}
	}
	assertCodexResourcesReleased(t, f, snapshot)
	assertCodexSubscriptionBoundary(t, f, snapshot)
}
