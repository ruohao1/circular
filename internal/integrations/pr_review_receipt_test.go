package integrations_test

import "testing"

func TestAutomaticReviewRequiresFirstDeliveryAndOptIn(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		f := newDeliveryFixture(t, false)
		if _, err := f.pool.Exec(t.Context(), `INSERT INTO pr_review_settings(project_id,automatic) VALUES($1,$2)`, f.project, enabled); err != nil {
			t.Fatal(err)
		}
		f.queue(t)
		f.process(t)
		if _, err := f.pool.Exec(t.Context(), `UPDATE pr_review_settings SET automatic=true WHERE project_id=$1`, f.project); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if _, err := f.service.QueueGitHubRunDelivery(t.Context(), f.run); err != nil {
				t.Fatal(err)
			}
		}
		if worked, err := f.service.ProcessGitHubRunDelivery(t.Context()); err != nil || worked {
			t.Fatal(worked, err)
		}
		var count int
		if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM pr_review_launch_intents WHERE source_run_id=$1`, f.run).Scan(&count); err != nil {
			t.Fatal(err)
		}
		want := 0
		if enabled {
			want = 1
		}
		if count != want {
			t.Fatalf("enabled=%v: %d intents, want %d", enabled, count, want)
		}
	}
}
