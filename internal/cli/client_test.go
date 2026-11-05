package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
)

func TestAPIEndpointValidation(t *testing.T) {
	for _, endpoint := range []string{
		"https://graphql.hyperoptimizer.com", "http://localhost:3002", "http://127.0.0.1:3002", "http://[::1]:3002", "http://graphql.127.0.0.1.nip.io/", "http://graphql.127.0.0.1.nip.io:8080", "http://GRAPHQL.127.0.0.1.NIP.IO/",
	} {
		t.Run(endpoint, func(t *testing.T) {
			if _, err := newClient(endpoint); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, endpoint := range []string{
		"http://api.hyperoptimizer.com", "http://graphql.127.0.0.1.nip.io.attacker.example", "http://not-graphql.127.0.0.1.nip.io", "http://api.192.168.1.10.nip.io", "http://127.0.0.1.attacker.example", "http://graphql.127.0.0.1.nip.io@attacker.example", "http://user:password@graphql.127.0.0.1.nip.io", "http://graphql.127.0.0.1.nip.io?token=secret", "http://graphql.127.0.0.1.nip.io#fragment", "ftp://graphql.127.0.0.1.nip.io", "not-a-url",
	} {
		t.Run(endpoint, func(t *testing.T) {
			if _, err := newClient(endpoint); err == nil {
				t.Fatal("unsafe or malformed endpoint accepted")
			}
		})
	}
}

func TestGraphQLRedirectCannotForwardCredentials(t *testing.T) {
	calls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; t.Error("redirect target received request") }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/graphql", http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	client, err := newClient(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.Identity(authContext(context.Background(), "ho_secret")); err == nil || calls != 0 {
		t.Fatal("credential redirect was followed")
	}
}

func TestUserAgentIncludesLowercaseVersionAndSystem(t *testing.T) {
	previousVersion := Version
	Version = "1.2.3-RC1"
	t.Cleanup(func() { Version = previousVersion })
	var userAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userAgent = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":{"cliIdentity":{"keyId":"key","organizationId":"org","organizationSlug":"test","organizationName":"Test"}}}`)
	}))
	defer server.Close()
	client, err := newClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Identity(authContext(context.Background(), "test")); err != nil {
		t.Fatal(err)
	}
	if want := "hpo/1.2.3-rc1/" + runtime.GOOS; userAgent != want {
		t.Fatalf("User-Agent = %q; want %q", userAgent, want)
	}
}
