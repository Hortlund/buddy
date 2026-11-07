package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"buddy/internal/api"
	"buddy/internal/tasks"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func gmailSetupSteps(c *ui.Context, busy bool) {
	ui.Text(c, "Set up Google sign-in once for this Buddy server. Use the same Google Cloud project for each step.").
		FontSize(12).TextColor(c.Theme().TextMuted)
	steps := []struct {
		title, detail string
		links         [][2]string
	}{
		{"1. Enable Gmail access", "Select or create a project in Google Cloud, then enable the Gmail API.",
			[][2]string{{"Enable Gmail API", "https://console.cloud.google.com/apis/enableflow;apiid=gmail.googleapis.com"}}},
		{"2. Set up the consent screen", "Choose External for personal Gmail. While the app is in Testing, add your mailbox under Test users.",
			[][2]string{{"Open Branding", "https://console.cloud.google.com/auth/branding"}, {"Open Audience", "https://console.cloud.google.com/auth/audience"}}},
		{"3. Create a desktop client", "Choose Create Client → Desktop app, download the JSON, then import it below. You can import an existing desktop client right away.",
			[][2]string{{"Open OAuth clients", "https://console.cloud.google.com/auth/clients"}}},
	}
	services := c.Services()
	for i, step := range steps {
		ui.Column(c.Key(i)).Gap(4).Padding(7, 0).Children(func() {
			ui.Text(c, step.title).FontSize(13).FontWeight(650)
			ui.Text(c, step.detail).FontSize(12).TextColor(c.Theme().TextMuted)
			ui.Row(c).Gap(6).Wrap().Children(func() {
				for j, link := range step.links {
					ui.Button(c.Key(j), link[0]).Disabled(busy).OnClick(func() { services.OpenURL(link[1]) })
				}
			})
		})
	}
}

func (a *desktop) chooseGmailClient(services ui.Services) {
	v := &a.integrations
	if v.busy != "" || v.draft == nil || a.window == nil {
		return
	}
	v.busy, v.err = "import", ""
	win, client, draft := a.window, a.client, v.draft
	sharedSetup := a.serverProtocol >= api.IntegrationSignInProtocol
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent: win, Title: "Import a Google Desktop OAuth client", ButtonLabel: "Import",
			Filters: []mygo.FileFilter{{Name: "OAuth client JSON", Extensions: []string{"json"}}},
		})
		var id, secret string
		if err == nil && len(paths) != 0 {
			id, secret, err = readGoogleClient(paths[0])
			if err == nil && sharedSetup {
				err = client.SaveGoogleOAuthClient(a.ctx, id, secret)
			}
		}
		win.Update(func() {
			v.busy = ""
			if v.draft != draft || a.client != client {
				return
			}
			if err != nil {
				v.err = err.Error()
			} else if id != "" {
				if sharedSetup {
					v.catalog.GoogleSignIn = true
					a.finishIntegrationChange()
					if v.googleSetup {
						v.draft, v.editing, v.googleSetup = nil, "", false
						v.notice = "Google sign-in setup updated."
					} else {
						a.connectIntegration(services)
					}
				} else {
					draft.ClientID, draft.ClientSecret = id, secret
				}
			}
		})
	}()
}

func readGoogleClient(path string) (string, string, error) {
	invalid := errors.New("Choose the JSON file for a Google Desktop OAuth client.")
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 32<<10 {
		return "", "", invalid
	}
	file, err := os.Open(path)
	if err != nil {
		return "", "", invalid
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 32<<10+1))
	var oauth struct {
		Installed struct {
			ID     string `json:"client_id"`
			Secret string `json:"client_secret"`
		}
	}
	if err != nil || len(data) > 32<<10 || json.Unmarshal(data, &oauth) != nil ||
		!strings.HasSuffix(oauth.Installed.ID, ".apps.googleusercontent.com") {
		return "", "", invalid
	}
	return oauth.Installed.ID, oauth.Installed.Secret, nil
}

func bundledGoogleClient() api.GoogleOAuthClient {
	executable, err := os.Executable()
	if err != nil {
		return api.GoogleOAuthClient{}
	}
	dir := filepath.Dir(executable)
	contents := filepath.Dir(dir)
	if filepath.Base(dir) != "MacOS" || filepath.Base(contents) != "Contents" {
		return api.GoogleOAuthClient{}
	}
	id, secret, err := readGoogleClient(filepath.Join(contents, "Resources", "GmailDesktopClient.json"))
	if err != nil || secret == "" {
		return api.GoogleOAuthClient{}
	}
	return api.GoogleOAuthClient{ID: id, Secret: secret}
}

func configureGoogleSignIn(setup *api.ConnectionSetup, server, bundled api.GoogleOAuthClient) error {
	if server.ID != "" {
		setup.ClientID, setup.ClientSecret, setup.UseSavedGoogleClient = server.ID, "", true
		return nil
	}
	if bundled.ID != "" {
		setup.ClientID, setup.ClientSecret, setup.UseSavedGoogleClient = bundled.ID, bundled.Secret, false
		return nil
	}
	return errors.New("Set up Google sign-in for this Buddy server first.")
}

func (a *desktop) connectIntegration(services ui.Services) {
	v := &a.integrations
	if v.busy != "" || v.draft == nil || a.window == nil || !a.online {
		return
	}
	win, client, draft, id := a.window, a.client, v.draft, v.editing
	setup := *draft
	bundledGoogle := v.bundledGoogle
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Minute)
	sharedSetup := a.serverProtocol >= api.IntegrationSignInProtocol
	v.busy, v.err, v.notice = "connect", "", ""
	if setup.Source == tasks.GmailLabel {
		v.cancel = cancel
	}
	go func() {
		defer cancel()
		var err error
		if setup.Source == tasks.GmailLabel {
			if sharedSetup {
				var oauth api.GoogleOAuthClient
				oauth, err = client.GoogleOAuthClient(ctx)
				if err == nil {
					err = configureGoogleSignIn(&setup, oauth, bundledGoogle)
				}
			}
			if err == nil {
				setup, err = authorizeGmail(ctx, setup, func(link string) {
					win.Update(func() {
						if v.draft == draft && a.client == client && ctx.Err() == nil {
							services.OpenURL(link)
						}
					})
				})
			}
		}
		var connected tasks.ProviderConnection
		if err == nil {
			connected, err = client.ConnectProvider(ctx, id, setup)
		}
		win.Update(func() {
			v.busy, v.cancel = "", nil
			a.finishIntegrationChange()
			if v.draft != draft || a.client != client {
				return
			}
			if err != nil {
				v.err = errorMessage(err, err.Error())
				return
			}
			v.draft, v.editing, v.notice = nil, "", "Connected "+connected.Name+"."
		})
	}()
}

func authorizeGmail(ctx context.Context, setup api.ConnectionSetup, open func(string)) (api.ConnectionSetup, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return setup, errors.New("Couldn’t open a local port for Google sign-in.")
	}
	defer listener.Close()
	address := listener.Addr().String()
	setup.Redirect = "http://" + address + "/callback"
	setup.Verifier = rand.Text() + rand.Text()
	state := rand.Text()
	digest := sha256.Sum256([]byte(setup.Verifier))
	params := url.Values{
		"client_id": {setup.ClientID}, "redirect_uri": {setup.Redirect}, "response_type": {"code"},
		"scope": {"openid https://www.googleapis.com/auth/gmail.readonly"}, "state": {state},
		"access_type": {"offline"}, "prompt": {"consent select_account"}, "code_challenge_method": {"S256"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(digest[:])},
	}
	type callback struct {
		code string
		err  error
	}
	result := make(chan callback, 1)
	deliver := func(response callback) {
		select {
		case result <- response:
		default:
		}
	}
	server := &http.Server{
		ReadHeaderTimeout: 3 * time.Second, WriteTimeout: 5 * time.Second, MaxHeaderBytes: 16 << 10,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if r.Method != http.MethodGet || r.Host != address || r.URL.Path != "/callback" ||
				subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
				http.Error(w, "This sign-in request is invalid.", http.StatusBadRequest)
				return
			}
			reply := callback{code: q.Get("code")}
			message := "Return to Buddy to finish connecting Gmail."
			if reply.code == "" || len(reply.code) > 8192 || q.Get("error") != "" {
				reply = callback{err: errors.New("Google sign-in was declined or interrupted. Try again when ready.")}
				message = "Google sign-in did not finish. Return to Buddy to try again."
			}
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Referrer-Policy", "no-referrer")
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = io.WriteString(w, message)
			_ = http.NewResponseController(w).Flush()
			deliver(reply)
		}),
	}
	defer server.Close()
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			deliver(callback{err: errors.New("The local Google sign-in listener stopped.")})
		}
	}()
	open("https://accounts.google.com/o/oauth2/v2/auth?" + params.Encode())
	select {
	case <-ctx.Done():
		return setup, errors.New("Google sign-in timed out or was cancelled.")
	case response := <-result:
		setup.Code = response.code
		return setup, response.err
	}
}
