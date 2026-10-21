package postgres_test

import (
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/completion"
	"github.com/ruohao1/circular/internal/postgres"
	"sync"
	"testing"
	"time"
)

func TestCompletionEvidenceLeaseAndReplacement(t *testing.T) {
	for _, change := range []string{"none", "expired", "binding", "reopen", "reconnect", "review"} {
		t.Run(change, func(t *testing.T) {
			f, task, session := externalReviewFixture(t)
			identity := uuid.New()
			if _, err := f.p.Exec(t.Context(), `INSERT INTO provider_identities(id,provider,app_client_id,account_id,account_name,actor_id,actor_name,status) VALUES($1,'github','fixture','42','Fixture','501','Bot','available')`, identity); err != nil {
				t.Fatal(err)
			}
			if _, err := f.p.Exec(t.Context(), `INSERT INTO integration_identity_bindings(project_id,provider,identity_id) VALUES($1,'github',$2)`, f.project, identity); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			claim := func() (postgres.CompletionRefresh, error) {
				return workTx(t, f.p, func(tx pgx.Tx) (postgres.CompletionRefresh, error) {
					return postgres.ClaimCompletionEvidence(t.Context(), tx, task, true, now)
				})
			}
			var wg sync.WaitGroup
			leases := make(chan postgres.CompletionRefresh, 2)
			for range 2 {
				wg.Go(func() {
					v, err := claim()
					if err != nil {
						t.Error(err)
					}
					leases <- v
				})
			}
			wg.Wait()
			close(leases)
			var first postgres.CompletionRefresh
			count := 0
			for v := range leases {
				if v.Owner != uuid.Nil {
					first = v
					count++
				}
			}
			if count != 1 || first.View.Binding == nil || first.View.Binding.Number != 1 {
				t.Fatal(count, first)
			}
			record := func(snapshot postgres.CompletionRefresh, at time.Time) (bool, error) {
				return workTx(t, f.p, func(tx pgx.Tx) (bool, error) {
					return postgres.RecordCompletionEvidence(t.Context(), tx, snapshot, completion.Observation{PR: snapshot.PR, ErrorCode: "provider_unavailable"}, at, time.Minute)
				})
			}
			switch change {
			case "expired":
				now = now.Add(31 * time.Second)
			case "binding":
				_, err := workTx(t, f.p, func(tx pgx.Tx) (bool, error) {
					return true, postgres.BindCompletionPR(t.Context(), tx, task, session, first.View.Revision, 8, *first.View.Identity)
				})
				if err != nil {
					t.Fatal(err)
				}
			case "reopen":
				if _, err := f.move(t, task, "ready", "end", nil); err != nil {
					t.Fatal(err)
				}
			case "reconnect":
				if _, err := f.p.Exec(t.Context(), `UPDATE provider_identities SET generation=generation+1 WHERE id=$1`, identity); err != nil {
					t.Fatal(err)
				}
			case "review":
				if _, err := f.p.Exec(t.Context(), `UPDATE task_completions SET review_revision=review_revision+1 WHERE session_id=$1`, session); err != nil {
					t.Fatal(err)
				}
			}
			stored, err := record(first, now)
			if err != nil || stored != (change == "none") {
				t.Fatal(stored, err)
			}
			view := completionView(t, f, task)
			if change == "none" && (view.Observation == nil || view.Observation.ErrorCode != "provider_unavailable") {
				t.Fatal(view)
			}
			if change == "expired" {
				second, err := workTx(t, f.p, func(tx pgx.Tx) (postgres.CompletionRefresh, error) {
					return postgres.ClaimCompletionEvidence(t.Context(), tx, task, true, now)
				})
				if err != nil || second.Owner == uuid.Nil {
					t.Fatal(second, err)
				}
				if ok, err := record(second, now); err != nil || !ok {
					t.Fatal(ok, err)
				}
				if ok, err := record(first, now); err != nil || ok {
					t.Fatal("slow old response overwrote newer", ok, err)
				}
			}
		})
	}
}
