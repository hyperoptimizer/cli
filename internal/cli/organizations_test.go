package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyperoptimizer/cli/internal/api"
)

func organizationFixture() []api.Organization {
	slug := "research"
	return []api.Organization{{Id: "org_a", Slug: &slug, Name: "Research", Plan: "Starter", SubscriptionStatus: "active"}, {Id: "org_b", Name: "Team", Plan: "Pro", SubscriptionStatus: "trialing"}}
}
func TestOrganizationDefaultIsBoundToCredentialAndOverrideDoesNotChangeIt(t *testing.T) {
	var selected string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		selected = r.Header.Get("X-HPO-Organization")
		writeGraphQL(w, "cliIdentity", map[string]any{"keyId": "key", "organizations": organizationFixture()})
	}))
	defer server.Close()
	app, _, _ := testApp(server.URL)
	setEnvironmentKey(app, "", false)
	app.CredentialFile = filepath.Join(t.TempDir(), "key")
	if err := app.saveCredential("ho_user"); err != nil {
		t.Fatal(err)
	}
	if err := app.saveDefaultOrganization("ho_user", "org_a"); err != nil {
		t.Fatal(err)
	}
	if value, err := app.defaultOrganization("ho_other"); err != nil || value != "" {
		t.Fatalf("default leaked across users: %s %v", value, err)
	}
	for _, override := range []string{"", "org_b"} {
		app.Organization = override
		client, ctx, _, err := app.authorized(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if _, err = client.Identity(ctx); err != nil {
			t.Fatal(err)
		}
		want := "org_a"
		if override != "" {
			want = override
		}
		if selected != want {
			t.Fatalf("header %q, want %q", selected, want)
		}
	}
	if value, err := app.defaultOrganization("ho_user"); err != nil || value != "org_a" {
		t.Fatalf("override persisted: %s %v", value, err)
	}
}
func TestOrganizationListWorksWithRemovedDefaultAndShowsPlans(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-HPO-Organization") != "" {
			t.Error("list used stale default")
		}
		writeGraphQL(w, "cliIdentity", map[string]any{"keyId": "key", "organizations": organizationFixture()})
	}))
	defer server.Close()
	app, out, _ := testApp(server.URL)
	setEnvironmentKey(app, "", false)
	app.CredentialFile = filepath.Join(t.TempDir(), "key")
	if err := app.saveCredential("ho_user"); err != nil {
		t.Fatal(err)
	}
	if err := app.saveDefaultOrganization("ho_user", "removed"); err != nil {
		t.Fatal(err)
	}
	credentialPath := app.CredentialFile
	cmd := app.Command()
	cmd.SetArgs([]string{"org", "list", "--credential-file", credentialPath, "--output", "plain"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Starter") || !strings.Contains(out.String(), "trialing") || !strings.Contains(out.String(), "org_b") {
		t.Fatal(out.String())
	}
}
func TestPersonalLoginPromptsForDefaultOrganization(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeGraphQL(w, "cliIdentity", map[string]any{"keyId": "key", "organizations": organizationFixture()})
	}))
	defer server.Close()
	app, _, _ := testApp(server.URL)
	app.CredentialFile = filepath.Join(t.TempDir(), "key")
	app.Interactive = func() bool { return true }
	app.SelectOrganization = func(_ context.Context, orgs []api.Organization) (string, error) {
		if len(orgs) != 2 || orgs[0].Plan != "Starter" {
			t.Fatal(orgs)
		}
		return "org_b", nil
	}
	client, err := newClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err = app.finishPersonalLogin(context.Background(), client, "ho_user"); err != nil {
		t.Fatal(err)
	}
	if value, err := app.defaultOrganization("ho_user"); err != nil || value != "org_b" {
		t.Fatalf("default: %s %v", value, err)
	}
}
func TestOrganizationUseRejectsUnavailableOrganizationWithoutChangingDefault(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeGraphQL(w, "cliIdentity", map[string]any{"keyId": "key", "organizations": organizationFixture()})
	}))
	defer server.Close()
	app, _, _ := testApp(server.URL)
	setEnvironmentKey(app, "", false)
	app.CredentialFile = filepath.Join(t.TempDir(), "key")
	if err := app.saveCredential("ho_user"); err != nil {
		t.Fatal(err)
	}
	if err := app.saveDefaultOrganization("ho_user", "org_a"); err != nil {
		t.Fatal(err)
	}
	credentialPath := app.CredentialFile
	cmd := app.Command()
	cmd.SetArgs([]string{"org", "use", "unavailable", "--credential-file", credentialPath})
	if err := cmd.Execute(); err == nil {
		t.Fatal("unknown organization accepted")
	}
	if value, err := app.defaultOrganization("ho_user"); err != nil || value != "org_a" {
		t.Fatalf("changed default: %s %v", value, err)
	}
}
