package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	"buddy/internal/api"
	"buddy/internal/tasks"

	"github.com/egoist/mygo/ui"
)

func TestGmailAuthorizationUsesLoopbackStateAndPKCE(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	bundled := api.GoogleOAuthClient{ID: "fixture.apps.googleusercontent.com", Secret: "fixture-client-secret"}
	var setup api.ConnectionSetup
	if err := configureGoogleSignIn(&setup, api.GoogleOAuthClient{}, bundled); err != nil {
		t.Fatal(err)
	}
	var params url.Values
	setup, err := authorizeGmail(ctx, setup, func(link string) {
		u, err := url.Parse(link)
		if err != nil || u.Host != "accounts.google.com" {
			t.Fatalf("unexpected browser URL: %v", err)
		}
		params = u.Query()
		for _, state := range []string{"incorrect", params.Get("state")} {
			query := url.Values{"state": {state}, "code": {"fixture-code"}}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, params.Get("redirect_uri")+"?"+query.Encode(), nil)
			if err != nil {
				t.Fatal(err)
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, res.Body)
			res.Body.Close()
			if state == "incorrect" && res.StatusCode != http.StatusBadRequest ||
				state != "incorrect" && res.StatusCode != http.StatusOK {
				t.Fatalf("unexpected callback status %d", res.StatusCode)
			}
		}
	})
	if err != nil || setup.Code != "fixture-code" || params.Get("access_type") != "offline" ||
		params.Get("client_id") != bundled.ID || params.Has("client_secret") ||
		setup.ClientSecret != bundled.Secret || setup.UseSavedGoogleClient {
		t.Fatalf("authorization did not complete: %v", err)
	}
	digest := sha256.Sum256([]byte(setup.Verifier))
	if params.Get("code_challenge_method") != "S256" || params.Get("code_challenge") != base64.RawURLEncoding.EncodeToString(digest[:]) {
		t.Fatal("PKCE challenge did not match its verifier")
	}
}

func TestBundledGoogleClientEnablesSignInOnNewBuddyHome(t *testing.T) {
	a := &desktop{online: true, serverProtocol: api.DesktopProtocol}
	a.integrations.bundledGoogle = api.GoogleOAuthClient{ID: "beta.apps.googleusercontent.com", Secret: "fixture"}
	view := ui.NewTester(a.integrationSettings, 760, 860)
	if err := view.Click("Continue with Google"); err != nil {
		t.Fatal(err)
	}
	if a.integrations.draft == nil || a.integrations.draft.Source != tasks.GmailLabel ||
		view.HasText("Import JSON and sign in") || view.HasText("Enable Gmail API") {
		t.Fatalf("fresh server showed manual setup: %v", view.Texts())
	}

	var setup api.ConnectionSetup
	server := api.GoogleOAuthClient{ID: "server.apps.googleusercontent.com"}
	if err := configureGoogleSignIn(&setup, server, a.integrations.bundledGoogle); err != nil ||
		setup.ClientID != server.ID || setup.ClientSecret != "" || !setup.UseSavedGoogleClient {
		t.Fatal("the existing server registration was not preserved")
	}
}

func TestConnectionsExposeNamedAccountsAndProviderSpecificSetup(t *testing.T) {
	a := &desktop{online: true, serverProtocol: api.IntegrationsProtocol}
	a.integrations.catalog.Connections = []tasks.ProviderConnection{
		{ID: "work", Source: tasks.GmailLabel, Name: "Work mailbox", Account: "work@example.test", Connected: true},
		{ID: "personal", Source: tasks.GmailLabel, Name: "Personal mailbox", Account: "me@example.test", Connected: true},
		{ID: "old", Source: tasks.GrafanaAlerts, Name: "Old cluster"},
	}
	view := ui.NewTester(a.integrationSettings, 760, 860)
	if !view.HasText("Work mailbox") || !view.HasText("Personal mailbox") || view.HasText("Old cluster") {
		t.Fatalf("unexpected account list: %v", view.Texts())
	}
	if err := view.Click("Show disconnected"); err != nil {
		t.Fatal(err)
	}
	if !view.HasText("Old cluster") || !view.HasText("Reconnect") {
		t.Fatalf("disconnected connection cannot be recovered: %v", view.Texts())
	}
	if err := view.Click("Add Gmail"); err != nil {
		t.Fatal(err)
	}
	if !view.HasText("Import OAuth client JSON") || !view.HasText("Continue with Google") {
		t.Fatalf("missing Gmail sign-in setup: %v", view.Texts())
	}
}

func TestConnectionsOfferSavedGoogleSetupAndGitHubCLISignIn(t *testing.T) {
	a := &desktop{online: true, serverProtocol: api.IntegrationSignInProtocol}
	a.integrations.catalog.GoogleSignIn = true
	a.integrations.catalog.GitHubCLI = true
	view := ui.NewTester(a.integrationSettings, 760, 860)
	if !view.HasText("Continue with Google") {
		t.Fatal("Gmail did not offer direct sign-in")
	}
	if err := view.Click("Add GitHub"); err != nil {
		t.Fatal(err)
	}
	if !view.HasText("Use GitHub CLI sign-in") || !view.HasText("Use a token instead") || view.HasText("Connection name (optional)") {
		t.Fatalf("unexpected GitHub sign-in view: %v", view.Texts())
	}
	if err := view.Click("Use a token instead"); err != nil {
		t.Fatal(err)
	}
	if !view.HasText("Create a GitHub token") || !view.HasText("Test and connect") {
		t.Fatalf("token setup was not available: %v", view.Texts())
	}
}

func TestGmailFirstRunShowsDirectSetupSteps(t *testing.T) {
	a := &desktop{online: true, serverProtocol: api.DesktopProtocol}
	view := ui.NewTester(a.integrationSettings, 900, 1000)
	if err := view.Click("Set up Gmail"); err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"Enable Gmail API", "Open Branding", "Open Audience", "Open OAuth clients", "Import JSON and sign in"} {
		if _, ok := view.Find(label); !ok {
			t.Fatalf("Gmail setup is missing %q", label)
		}
	}
	if view.HasText("Google Cloud setup") || view.HasText("Continue with Google") {
		t.Fatalf("unexpected first-run actions: %v", view.Texts())
	}
}
