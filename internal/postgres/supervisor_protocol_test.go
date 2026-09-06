package postgres_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ruohao1/circular/internal/execution"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/testsupport"
)

type protocolChunk struct {
	Stream string `json:"stream"`
	Data   []byte `json:"data"`
}
type protocolScript struct {
	Chunks   []protocolChunk `json:"chunks"`
	ExitCode int             `json:"exit_code"`
}

func TestSupervisorPreservesJSONLValidationAndFailureReplay(t *testing.T) {
	delta := `{"protocol_version":1,"run_id":"@RUN_ID@","source":"fake-container-workload","type":"agent.message.delta","data":{"delta":"hello 🌍"}}` + "\n"
	usage := `{"protocol_version":1,"run_id":"@RUN_ID@","source":"fake-container-workload","type":"usage.updated","data":{"input_tokens":0,"output_tokens":1}}` + "\n"
	for _, test := range []struct {
		name   string
		chunks []protocolChunk
		code   int
	}{
		{"split_unicode", []protocolChunk{{"stdout", []byte(delta[:len(delta)-7])}, {"stdout", []byte(delta[len(delta)-7:])}}, 0},
		{"duplicate_top_level", []protocolChunk{{"stdout", []byte(strings.Replace(delta, `"protocol_version":1`, `"protocol_version":1,"protocol_version":1`, 1))}}, 0},
		{"duplicate_nested", []protocolChunk{{"stdout", []byte(strings.Replace(delta, `"delta":"hello 🌍"`, `"delta":"first","delta":"last"`, 1))}}, 0},
		{"lone_surrogate", []protocolChunk{{"stdout", []byte(strings.Replace(delta, `hello 🌍`, `\ud800`, 1))}}, 0},
		{"float_overflow", []protocolChunk{{"stdout", []byte(strings.Replace(usage, `"input_tokens":0`, `"input_tokens":1e999`, 1))}}, 0},
		{"nan", []protocolChunk{{"stdout", []byte(strings.Replace(usage, `"input_tokens":0`, `"input_tokens":NaN`, 1))}}, 0},
		{"boolean_usage", []protocolChunk{{"stdout", []byte(strings.Replace(usage, `"input_tokens":0`, `"input_tokens":true`, 1))}}, 0},
		{"float_usage", []protocolChunk{{"stdout", []byte(strings.Replace(usage, `"input_tokens":0`, `"input_tokens":1.0`, 1))}}, 0},
		{"negative_usage", []protocolChunk{{"stdout", []byte(strings.Replace(usage, `"input_tokens":0`, `"input_tokens":-1`, 1))}}, 0},
		{"large_integer", []protocolChunk{{"stdout", []byte(strings.Replace(usage, `"input_tokens":0`, `"input_tokens":184467440737095516160000`, 1))}}, 0},
		{"foreign_run", []protocolChunk{{"stdout", []byte(strings.Replace(delta, "@RUN_ID@", "00000000-0000-0000-0000-000000000000", 1))}}, 0},
		{"unsupported_source", []protocolChunk{{"stdout", []byte(strings.Replace(delta, "fake-container-workload", "untrusted", 1))}}, 0},
		{"unsupported_type", []protocolChunk{{"stdout", []byte(strings.Replace(delta, "agent.message.delta", "run.completed", 1))}}, 0},
		{"float_version", []protocolChunk{{"stdout", []byte(strings.Replace(delta, `"protocol_version":1`, `"protocol_version":1.0`, 1))}}, 0},
		{"incomplete_line", []protocolChunk{{"stdout", []byte(strings.TrimSuffix(delta, "\n"))}}, 0},
		{"oversized_line", []protocolChunk{{"stdout", []byte(strings.Repeat("x", 1024*1024+1))}}, 0},
		{"invalid_utf8", []protocolChunk{{"stdout", []byte{0xff, '\n'}}}, 0},
		{"trailing_json", []protocolChunk{{"stdout", []byte(strings.TrimSuffix(delta, "\n") + "{}\n")}}, 0},
		{"progress_then_bad_line", []protocolChunk{{"stdout", []byte(delta + "not-json\n")}}, 20},
		{"invalid_input", []protocolChunk{{"stderr", []byte(`{"protocol_version":1,"error":{"code":"invalid_input","message":"safe fixture"}}` + "\n")}}, 2},
		{"invalid_error_code", []protocolChunk{{"stderr", []byte(`{"protocol_version":1,"error":{"code":"other","message":"fixture"}}` + "\n")}}, 2},
		{"process_failure", nil, 23},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "output.json")
			chunks := test.chunks
			if chunks == nil {
				chunks = []protocolChunk{}
			}
			data, err := json.Marshal(protocolScript{chunks, test.code})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			f := newSupervisorFixture(t, map[string]any{"output_script": path})
			claim := acquire(t, postgres.NewQueue(f.pool), "supervisor-owner")
			supervisor, err := execution.NewSupervisor(f.pool, "supervisor-owner", f.config)
			if err != nil {
				t.Fatal(err)
			}
			_ = supervisor.Execute(t.Context(), *claim, "supervisor-owner")
			// Frozen protocol expectations preserve the established wire contract.
			failures := map[string]string{
				"duplicate_top_level":    "fake backend event contains a duplicate JSON field at stdout line 1",
				"duplicate_nested":       "fake backend event contains a duplicate JSON field at stdout line 1",
				"lone_surrogate":         "fake backend event contains text that is not valid Unicode at stdout line 1",
				"float_overflow":         "fake backend line is not valid JSON at stdout line 1",
				"nan":                    "fake backend line is not valid JSON at stdout line 1",
				"boolean_usage":          "fake backend usage update has invalid data at stdout line 1",
				"float_usage":            "fake backend usage update has invalid data at stdout line 1",
				"negative_usage":         "fake backend usage update has invalid data at stdout line 1",
				"foreign_run":            "fake backend event does not match the executing run at stdout line 1",
				"unsupported_source":     "fake backend event has an unsupported source at stdout line 1",
				"unsupported_type":       "fake backend event has an unsupported type at stdout line 1",
				"float_version":          "fake backend event uses an unsupported protocol version at stdout line 1",
				"incomplete_line":        "fake backend ended with an incomplete JSON line at stdout line 1",
				"oversized_line":         "fake backend JSON line exceeded the line limit at stdout line 1",
				"invalid_utf8":           "fake backend emitted an invalid UTF-8 JSON line at stdout line 1",
				"trailing_json":          "fake backend emitted an invalid UTF-8 JSON line at stdout line 1",
				"progress_then_bad_line": "fake backend emitted an invalid UTF-8 JSON line at stdout line 2",
				"invalid_input":          "fake backend reported invalid_input",
				"invalid_error_code":     "fake backend error has unsupported data at stderr line 1",
				"process_failure":        "fake backend exited with code 23",
			}
			failure := failures[test.name]
			snapshot := testsupport.Observe(t, f.pool, f.id)
			snapshot.AssertReplay(t)
			status := "succeeded"
			if failure != "" {
				status = "failed"
			}
			if snapshot.Run.Status != status || snapshot.Run.WorkerID != nil || snapshot.Workspace == nil || snapshot.Workspace.Status != "released" {
				t.Fatalf("protocol outcome: %+v", snapshot)
			}
			if failure == "" {
				if snapshot.Run.Error != nil {
					t.Fatal(*snapshot.Run.Error)
				}
			} else {
				if snapshot.Run.Error == nil || *snapshot.Run.Error != failure {
					t.Fatalf("failure: %v; expected %s", snapshot.Run.Error, failure)
				}
			}
			decode := func(line string) map[string]any {
				var doc map[string]any
				d := json.NewDecoder(strings.NewReader(strings.ReplaceAll(line, "@RUN_ID@", f.id.String())))
				d.UseNumber()
				if err := d.Decode(&doc); err != nil {
					t.Fatal(err)
				}
				return doc
			}
			expected := []map[string]any{}
			switch test.name {
			case "split_unicode", "progress_then_bad_line":
				expected = append(expected, decode(delta))
			case "large_integer":
				expected = append(expected, decode(string(test.chunks[0].Data)))
			}
			actual := []map[string]any{}
			for _, e := range snapshot.Events {
				if e.Source == "fake-container-workload" {
					actual = append(actual, e.Raw)
					testsupport.AssertJSON(t, e.Data, e.Raw["data"])
					if e.Type != e.Raw["type"] {
						t.Fatal(e)
					}
				}
			}
			testsupport.AssertJSON(t, actual, expected)
			var expectedRaw map[string]any
			switch test.name {
			case "boolean_usage", "float_usage", "negative_usage", "foreign_run", "unsupported_source", "unsupported_type", "float_version", "invalid_input", "invalid_error_code":
				expectedRaw = decode(string(test.chunks[0].Data))
			}
			if failure != "" {
				if snapshot.Count("run.failed") != 1 {
					t.Fatal(snapshot.Types())
				}
				for _, e := range snapshot.Events {
					if e.Type == "run.failed" {
						testsupport.AssertJSON(t, e.Raw, expectedRaw)
					}
				}
			}
		})
	}
}
