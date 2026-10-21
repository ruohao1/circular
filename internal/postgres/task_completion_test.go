package postgres_test

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/completion"
	"github.com/ruohao1/circular/internal/postgres"
)

func completionView(t *testing.T, f planningFixture, id uuid.UUID) completion.View {
	t.Helper()
	v, err := workTx(t, f.p, func(tx pgx.Tx) (completion.View, error) {
		return postgres.InspectTaskCompletion(t.Context(), tx, id, time.Now().UTC())
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func finishReport(t *testing.T, f planningFixture, in postgres.WorkFinishInput) (postgres.WorkChangeResult, error) {
	t.Helper()
	return workTx(t, f.p, func(tx pgx.Tx) (postgres.WorkChangeResult, error) {
		return postgres.FinishExternalWork(t.Context(), tx, in, workNow.Add(time.Minute))
	})
}
func TestCompletionFinishAndLostAcknowledgement(t *testing.T) {
	for _, mode := range []string{"direct_completion", "review_required"} {
		t.Run(mode, func(t *testing.T) {
			f := planningSeed(t)
			id := f.task(t, "Implement", "ready", "{}")
			completionPolicy(t, f, id, mode)
			if mode == "review_required" {
				completionChecks(t, f, 0, "Tests")
			}
			claim := externalClaim(id)
			claim.ExpectedRevision, _ = f.versions(t, id)
			if _, err := claimWork(t, f, claim); err != nil {
				t.Fatal(err)
			}
			in := postgres.WorkFinishInput{Credential: postgres.WorkCredential{SessionID: claim.SessionID, Secret: claim.Secret}, OperationID: uuid.New(), Outcome: "succeeded"}
			if _, err := finishReport(t, f, in); err == nil {
				t.Fatal("success without report")
			}
			in.Report = &completion.Report{Result: "Implemented"}
			if _, err := finishReport(t, f, in); err == nil {
				t.Fatal("missing evidence accepted")
			}
			if currentWork(t, f, id) == nil {
				t.Fatal("bad finish released owner")
			}
			in.Report.Evidence = []completion.Evidence{{Key: "tests", Detail: "Passed"}}
			in.Summary = "Conflicting result"
			if _, err := finishReport(t, f, in); err == nil {
				t.Fatal("conflicting summary")
			}
			in.Summary = ""
			first, err := finishReport(t, f, in)
			if err != nil || first.Receipt.Reason != "Implemented" || first.Session.ReleasedAt == nil {
				t.Fatal(first, err)
			}
			var status string
			if err = f.p.QueryRow(t.Context(), `SELECT status FROM tasks WHERE id=$1`, id).Scan(&status); err != nil {
				t.Fatal(err)
			}
			want := "completed"
			if mode == "review_required" {
				want = "in_review"
			}
			if status != want {
				t.Fatal(status, want)
			}
			view := completionView(t, f, id)
			if view.Report == nil || view.Report.Result != "Implemented" {
				t.Fatal(view)
			}
			if _, err = f.move(t, id, "ready", "end", nil); err != nil {
				t.Fatal(err)
			}
			var cleared bool
			if err = f.p.QueryRow(t.Context(), `SELECT completion_session_id IS NULL FROM tasks WHERE id=$1`, id).Scan(&cleared); err != nil || !cleared {
				t.Fatal("reopen retained promotion pointer", err)
			}
			newer := externalClaim(id)
			newer.ExpectedRevision, _ = f.versions(t, id)
			if _, err = claimWork(t, f, newer); err != nil {
				t.Fatal(err)
			}
			revision, _ := f.versions(t, id)
			replay, err := finishReport(t, f, in)
			if err != nil || !reflect.DeepEqual(first.Receipt, replay.Receipt) {
				t.Fatal("lost acknowledgement changed", replay, err)
			}
			current := currentWork(t, f, id)
			after, _ := f.versions(t, id)
			if current == nil || current.ID != newer.SessionID || after != revision {
				t.Fatal("replay touched newer work", current, after, revision)
			}
		})
	}
}
func TestCompletionTaskMoveGuard(t *testing.T) {
	f := planningSeed(t)
	id := f.task(t, "Work", "ready", "{}")
	for _, stage := range []string{"in_review", "completed"} {
		if _, err := f.move(t, id, stage, "end", nil); err == nil {
			t.Fatal("manual completion bypass", stage)
		}
	}
	if _, err := claimWork(t, f, externalClaim(id)); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"open", "ready", "in_review", "completed", "cancelled"} {
		_, err := f.move(t, id, stage, "end", nil)
		var conflict *postgres.CompletionConflict
		if !errors.As(err, &conflict) {
			t.Fatal("owner stage change", stage, err)
		}
	}
	if _, err := f.move(t, id, "in_progress", "start", nil); err != nil {
		t.Fatal("owned reorder rejected", err)
	}
	if currentWork(t, f, id) == nil {
		t.Fatal("move released owner")
	}
}
func TestCompletionFinishRacesReopenAndPolicyEdit(t *testing.T) {
	for _, operation := range []string{"reopen", "policy"} {
		t.Run(operation, func(t *testing.T) {
			f := planningSeed(t)
			id := f.task(t, "Race", "ready", "{}")
			claim := externalClaim(id)
			if _, err := claimWork(t, f, claim); err != nil {
				t.Fatal(err)
			}
			in := postgres.WorkFinishInput{Credential: postgres.WorkCredential{SessionID: claim.SessionID, Secret: claim.Secret}, OperationID: uuid.New(), Outcome: "succeeded", Report: &completion.Report{Result: "Done"}}
			start := make(chan struct{})
			var wg sync.WaitGroup
			var finishErr, otherErr error
			wg.Go(func() { <-start; _, finishErr = finishReport(t, f, in) })
			wg.Go(func() {
				<-start
				if operation == "reopen" {
					_, otherErr = f.move(t, id, "ready", "end", nil)
				} else {
					_, otherErr = workTx(t, f.p, func(tx pgx.Tx) (bool, error) {
						return true, postgres.SetTaskCompletionPolicy(t.Context(), tx, id, 2, completion.Policy{Mode: "direct_completion"})
					})
				}
			})
			close(start)
			wg.Wait()
			if finishErr != nil {
				t.Fatal(finishErr)
			}
			if operation == "policy" && !errors.Is(otherErr, postgres.ErrTaskPlanningConflict) {
				t.Fatal(otherErr)
			}
			var status string
			if err := f.p.QueryRow(t.Context(), `SELECT status FROM tasks WHERE id=$1`, id).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if operation == "reopen" && otherErr == nil {
				if status != "ready" {
					t.Fatal("old promotion won over reopen", status)
				}
			} else if status != "completed" {
				t.Fatal(status, otherErr)
			}
			if currentWork(t, f, id) != nil {
				t.Fatal("finished owner remains")
			}
		})
	}
}
func TestCompletionFinishLegacyDigest(t *testing.T) {
	in := postgres.WorkFinishInput{OperationID: uuid.MustParse("10000000-0000-4000-8000-000000000001"), Outcome: "succeeded", Summary: "Done"}
	raw, err := json.Marshal(in)
	if err != nil || string(raw) != `{"Credential":{"SessionID":"00000000-0000-0000-0000-000000000000"},"OperationID":"10000000-0000-4000-8000-000000000001","Outcome":"succeeded","Summary":"Done"}` {
		t.Fatal("nil-report historical command changed", string(raw), err)
	}
}

func TestCompletionLegacyFinishReceiptReplaysAfterReassignment(t *testing.T) {
	f := planningSeed(t)
	id := f.task(t, "Legacy finish", "ready", "{}")
	old := externalClaim(id)
	if _, err := claimWork(t, f, old); err != nil {
		t.Fatal(err)
	}
	in := postgres.WorkFinishInput{Credential: postgres.WorkCredential{SessionID: old.SessionID, Secret: old.Secret}, OperationID: uuid.New(), Outcome: "succeeded", Summary: "Historical result"}
	raw, _ := json.Marshal([]any{"finish", in})
	digest := sha256.Sum256(raw)
	receipt := uuid.New()
	if _, err := f.p.Exec(t.Context(), `UPDATE work_sessions SET outcome='succeeded',finished_at=claimed_at,released_at=claimed_at,release_reason='external finish' WHERE id=$1`, old.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Exec(t.Context(), `INSERT INTO work_session_receipts(id,session_id,operation_id,kind,input_digest,actor,reason,at) VALUES($1,$2,$3,'finished',$4,'external','Historical result',$5)`, receipt, old.SessionID, in.OperationID, digest[:], workNow); err != nil {
		t.Fatal(err)
	}
	if _, err := f.move(t, id, "ready", "end", nil); err != nil {
		t.Fatal(err)
	}
	next := externalClaim(id)
	next.ExpectedRevision, _ = f.versions(t, id)
	if _, err := claimWork(t, f, next); err != nil {
		t.Fatal(err)
	}
	replay, err := finishReport(t, f, in)
	if err != nil || replay.Receipt.ID != receipt {
		t.Fatal("historical nil-report replay rejected", replay, err)
	}
	if owner := currentWork(t, f, id); owner == nil || owner.ID != next.SessionID {
		t.Fatal("replay changed current owner", owner)
	}
}

func TestCompletionReportAtSerializedLimit(t *testing.T) {
	f := planningSeed(t)
	id := f.task(t, "Bounded report", "ready", "{}")
	policy := completion.Policy{Mode: "direct_completion"}
	report := completion.Report{Result: strings.Repeat("r", 4000)}
	for i := range 20 {
		key := fmt.Sprintf("e%d", i)
		policy.Requirements = append(policy.Requirements, completion.Requirement{Key: key, Description: "Evidence"})
		report.Evidence = append(report.Evidence, completion.Evidence{Key: key, Detail: strings.Repeat("界", 2000)})
	}
	for i := range 20 {
		report.Evidence[i].Links = []string{"https://example.org/" + strings.Repeat("a", 2000)}
		raw, _ := json.Marshal(report)
		if len(raw) >= completion.MaxReportBytes {
			link := report.Evidence[i].Links[0]
			report.Evidence[i].Links[0] = link[:len(link)-(len(raw)-completion.MaxReportBytes)]
			break
		}
	}
	raw, _ := json.Marshal(report)
	if len(raw) != completion.MaxReportBytes || completion.ValidateReport(policy, report) != nil {
		t.Fatal("invalid boundary fixture", len(raw))
	}
	_, err := workTx(t, f.p, func(tx pgx.Tx) (bool, error) {
		return true, postgres.SetTaskCompletionPolicy(t.Context(), tx, id, 1, policy)
	})
	if err != nil {
		t.Fatal(err)
	}
	claim := externalClaim(id)
	claim.ExpectedRevision = 2
	if _, err := claimWork(t, f, claim); err != nil {
		t.Fatal(err)
	}
	_, err = finishReport(t, f, postgres.WorkFinishInput{Credential: postgres.WorkCredential{SessionID: claim.SessionID, Secret: claim.Secret}, OperationID: uuid.New(), Outcome: "succeeded", Report: &report})
	if err != nil {
		t.Fatal("valid bounded report rejected by storage", err)
	}
}
