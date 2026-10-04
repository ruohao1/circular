package integrations_test

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/integrations"
)

func verifiedIdentity(t *testing.T, f fixture, provider string) string {
	t.Helper()
	id := uuid.NewString()
	_, err := f.pool.Exec(t.Context(), `INSERT INTO provider_identities(id,provider,app_client_id,account_id,account_name,actor_id,actor_name,actor_login,status,credentials) VALUES($1,$2,$3,'101','Fixture account','501','Circular','fixture[bot]','available',$4)`, id, provider, "fixture-"+provider, []byte("opaque-secret"))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestIdentityUpgradeStartsInUserMode(t *testing.T) {
	f := setup(t)
	f.connect(t, "github")
	got, err := f.service.Identity(t.Context(), f.project, "github")
	if err != nil || got.Mode != "user" || got.IdentityID != "" {
		t.Fatalf("legacy identity changed: %+v, %v", got, err)
	}
}

func TestIdentityBindingsShareGrantAndDetachWithoutFallback(t *testing.T) {
	f := setup(t)
	f.connect(t, "github")
	id := verifiedIdentity(t, f, "github")
	other := uuid.NewString()
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO projects(id,name) VALUES($1,'Second identity project')`, other); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 2)
	for _, project := range []string{f.project, other} {
		wg.Go(func() { _, err := f.service.BindIdentity(t.Context(), project, "github", id); failures <- err })
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := f.service.Identity(t.Context(), f.project, "github")
	if err != nil || got.Mode != "app" || got.Status != "enabled" || len(got.AffectedProjects) != 2 {
		t.Fatalf("missing shared identity: %+v %v", got, err)
	}
	public, _ := json.Marshal(got)
	if strings.Contains(string(public), "opaque-secret") || strings.Contains(string(public), "credentials") {
		t.Fatal("public identity leaked grant")
	}
	if err := f.service.DetachIdentity(t.Context(), f.project, "github"); err != nil {
		t.Fatal(err)
	}
	got, err = f.service.Identity(t.Context(), f.project, "github")
	if err != nil || got.Mode != "app" || got.Status != "disabled" || got.IdentityID != id {
		t.Fatal("detach reverted to human identity", got, err)
	}
	got, err = f.service.Identity(t.Context(), other, "github")
	if err != nil || got.Status != "enabled" {
		t.Fatal("detach revoked shared identity", got, err)
	}
}

func TestIdentityBindingRejectsWrongProviderAppAndDisabledGrant(t *testing.T) {
	f := setup(t)
	id := verifiedIdentity(t, f, "github")
	if _, err := f.service.BindIdentity(t.Context(), f.project, "linear", id); !errors.Is(err, integrations.ErrIdentityConflict) {
		t.Fatal("accepted different provider", err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE provider_identities SET app_client_id='foreign' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.BindIdentity(t.Context(), f.project, "github", id); !errors.Is(err, integrations.ErrIdentityConflict) {
		t.Fatal("accepted different app", err)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE provider_identities SET app_client_id='fixture-github',enabled=false WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.BindIdentity(t.Context(), f.project, "github", id); !errors.Is(err, integrations.ErrIdentityUnavailable) {
		t.Fatal("accepted disabled grant", err)
	}
}
