package migrate_test

import (
	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/migrate"
	"testing"
)

func TestIdentityUpgradeTreatsZeroAttemptPublicationsAsUncertainHistory(t *testing.T) {
	pool := legacyDatabase(t, 12)
	project, repo, agent, task, run, comment := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	_, err := pool.Exec(t.Context(), `WITH p AS (INSERT INTO projects(id,name) VALUES($1,'Publication upgrade') RETURNING id),
	 repo AS (INSERT INTO repositories(id,project_id,name,clone_url,default_branch,external_refs) VALUES($2,$1,'fixture/repo','https://github.com/fixture/repo.git','main','{}')),
	 a AS (INSERT INTO agents(id,project_id,name,backend,instructions,backend_config,enabled) VALUES($3,$1,'Coder','fake','','{}',true)),
	 t AS (INSERT INTO tasks(id,project_id,repository_id,title,description,status,external_refs) VALUES($4,$1,$2,'Task','','open','{}'))
	 INSERT INTO runs(id,task_id,agent_id,backend,status,attempt,external_refs) VALUES($5,$4,$3,'fake','succeeded',1,'{}')`, project, repo, agent, task, run)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(t.Context(), `INSERT INTO linear_run_updates(id,project_id,run_id,phase,outcome,account_id,issue_id,issue_url,summary,body,attempts) VALUES($1,$2,$3,'terminal','succeeded','workspace',$4,'https://linear.app/example/issue/TEST-1','Keep summary','Keep body',0)`, comment, project, run, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(t.Context(), `INSERT INTO github_run_deliveries(run_id,project_id,repository_id,github_repository_id,installation_id,repository_name,base_branch,branch,title,pull_request_started) VALUES($1::uuid,$2,$3,'202','101','fixture/repo','main','circular/run/'||($1::uuid)::text,'Keep title',true)`, run, project, repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	var uncertain, prUncertain, noAuthor bool
	var attempts, count int
	var body string
	err = pool.QueryRow(t.Context(), `SELECT u.legacy_reconcile,u.attempts,u.body,u.publisher IS NULL,d.legacy_reconcile,(SELECT count(*) FROM runs) FROM linear_run_updates u JOIN github_run_deliveries d ON d.run_id=u.run_id WHERE u.id=$1`, comment).Scan(&uncertain, &attempts, &body, &noAuthor, &prUncertain, &count)
	if err != nil || !uncertain || !prUncertain || !noAuthor || attempts != 0 || body != "Keep body" || count != 1 {
		t.Fatal("upgrade guessed author, history or launched a run", err)
	}
}
