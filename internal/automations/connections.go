package automations

import (
	"context"
	"errors"
	"net"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode"

	"buddy/internal/tasks"
)

const githubAPI = "https://api.github.com"

type ConnectionSetup struct {
	Name                 string `json:"name"`
	Source               string `json:"source"`
	Endpoint             string `json:"endpoint,omitempty"`
	Token                string `json:"token,omitempty"`
	ClientID             string `json:"client_id,omitempty"`
	ClientSecret         string `json:"client_secret,omitempty"`
	Code                 string `json:"code,omitempty"`
	Verifier             string `json:"verifier,omitempty"`
	Redirect             string `json:"redirect,omitempty"`
	UseGitHubCLI         bool   `json:"use_github_cli,omitempty"`
	UseSavedGoogleClient bool   `json:"use_saved_google_client,omitempty"`
	Collaboration        bool   `json:"collaboration,omitempty"`
}

type ScopeOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ConnectionCatalog struct {
	Connections  []tasks.ProviderConnection `json:"connections"`
	LegacyGitHub bool                       `json:"legacy_github,omitempty"`
	LegacyGmail  bool                       `json:"legacy_gmail,omitempty"`
	GoogleSignIn bool                       `json:"google_sign_in,omitempty"`
	GitHubCLI    bool                       `json:"github_cli,omitempty"`
}

func (s *Service) ConnectionCatalog(ctx context.Context) (ConnectionCatalog, error) {
	connections, err := s.store.ProviderConnections(ctx)
	if err != nil {
		return ConnectionCatalog{}, err
	}
	google, _, err := s.googleOAuthClient(ctx)
	_, ghErr := exec.LookPath("gh")
	return ConnectionCatalog{
		Connections: connections, LegacyGitHub: s.GitHubDelivery() != "unconfigured",
		LegacyGmail: s.config.GmailRefreshToken != "", GoogleSignIn: google != "", GitHubCLI: ghErr == nil,
	}, err
}

func (s *Service) Connect(ctx context.Context, id string, setup ConnectionSetup) (tasks.ProviderConnection, error) {
	var empty tasks.ProviderConnection
	c := tasks.ProviderCredentials{
		ProviderConnection: tasks.ProviderConnection{ID: id, Source: setup.Source, Name: strings.TrimSpace(setup.Name),
			Collaboration: setup.Collaboration},
		Secret: strings.TrimSpace(setup.Token),
	}
	if len(c.Name) > 100 || strings.ContainsAny(c.Name, "\r\n\x00") ||
		(c.Name == "" && c.Source != tasks.GitHubCI && c.Source != tasks.GmailLabel) {
		return empty, &tasks.InputError{Message: "Give the connection a short name."}
	}
	if len(c.Secret) > 8192 || strings.IndexFunc(c.Secret, unicode.IsSpace) >= 0 {
		return empty, &tasks.InputError{Message: "Enter the token without spaces or line breaks."}
	}
	var access string
	switch c.Source {
	case tasks.GitHubCI:
		if setup.UseGitHubCLI {
			var err error
			c.Secret, err = githubCLIToken(ctx)
			if err != nil {
				return empty, err
			}
		}
		if c.Secret == "" {
			return empty, &tasks.InputError{Message: "Use GitHub CLI sign-in or enter a GitHub token."}
		}
	case tasks.GmailLabel:
		u, err := url.Parse(setup.Redirect)
		if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" ||
			u.Path != "/callback" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
			strings.TrimSpace(setup.ClientID) == "" || setup.Code == "" || len(setup.Verifier) < 43 || len(setup.Verifier) > 128 {
			return empty, &tasks.InputError{Message: "Start a new Google sign-in from Buddy."}
		}
		c.ClientID, c.ClientSecret = setup.ClientID, setup.ClientSecret
		if setup.UseSavedGoogleClient {
			clientID, clientSecret, err := s.googleOAuthClient(ctx)
			if err != nil {
				return empty, err
			}
			if clientID != c.ClientID {
				return empty, &tasks.InputError{Message: "Google setup changed. Start sign-in again from Buddy."}
			}
			c.ClientSecret = clientSecret
		}
		tokens, err := s.exchangeGmail(ctx, url.Values{
			"grant_type": {"authorization_code"}, "code": {setup.Code}, "client_id": {c.ClientID},
			"client_secret": {c.ClientSecret}, "redirect_uri": {setup.Redirect}, "code_verifier": {setup.Verifier},
		})
		if err != nil {
			return empty, &tasks.InputError{Message: "Google couldn’t complete sign-in. Check the OAuth client and try again."}
		}
		if tokens.RefreshToken == "" {
			return empty, &tasks.InputError{Message: "Google didn’t grant offline access. Start sign-in again and allow access."}
		}
		c.Secret, access = tokens.RefreshToken, tokens.AccessToken
	case tasks.GrafanaAlerts, tasks.PrometheusAlerts:
		endpoint, err := monitoringEndpoint(setup.Endpoint, c.Secret != "")
		if err != nil {
			return empty, err
		}
		c.Endpoint = endpoint
		if c.Source == tasks.GrafanaAlerts && c.Secret == "" {
			return empty, &tasks.InputError{Message: "Enter a Grafana service account token with alert read access."}
		}
	default:
		return empty, &tasks.InputError{Message: "Choose a supported integration."}
	}
	account, identity, err := s.verifyConnection(ctx, c, access)
	if err != nil {
		return empty, &tasks.InputError{Message: err.Error()}
	}
	c.Account, c.Identity = account, identity
	if c.Name == "" {
		c.Name = account
	}
	return s.store.SaveProviderConnection(ctx, c)
}

func githubCLIToken(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "auth", "token", "--hostname", "github.com")
	for _, value := range cmd.Environ() {
		switch key, _, _ := strings.Cut(value, "="); key {
		case "GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_PROMPT_DISABLED":
		default:
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "GH_PROMPT_DISABLED=1")
	output, err := cmd.Output()
	token := strings.TrimSpace(string(output))
	if err != nil || token == "" || len(token) > 8192 || strings.IndexFunc(token, unicode.IsSpace) >= 0 {
		return "", &tasks.InputError{Message: "Sign in with gh auth login on the Buddy server, or connect with a GitHub token."}
	}
	return token, nil
}

type GoogleOAuthClient struct {
	ID     string `json:"client_id"`
	Secret string `json:"client_secret,omitempty"`
}

func (s *Service) googleOAuthClient(ctx context.Context) (string, string, error) {
	id, secret, err := s.store.GmailOAuthClient(ctx)
	if id == "" && err == nil {
		id, secret = s.config.GmailClientID, s.config.GmailClientSecret
	}
	return id, secret, err
}

func (s *Service) GoogleOAuthClient(ctx context.Context) (GoogleOAuthClient, error) {
	id, _, err := s.googleOAuthClient(ctx)
	return GoogleOAuthClient{ID: id}, err
}

func (s *Service) SaveGoogleOAuthClient(ctx context.Context, c GoogleOAuthClient) error {
	if !strings.HasSuffix(c.ID, ".apps.googleusercontent.com") || len(c.ID) > 256 || c.Secret == "" ||
		len(c.Secret) > 1024 || strings.IndexFunc(c.ID+c.Secret, unicode.IsSpace) >= 0 || strings.ContainsRune(c.ID+c.Secret, 0) {
		return &tasks.InputError{Message: "Import the JSON file for a Google Desktop OAuth client."}
	}
	return s.store.SaveGmailOAuthClient(ctx, c.ID, c.Secret)
}

func monitoringEndpoint(raw string, authenticated bool) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || len(raw) > 500 {
		return "", &tasks.InputError{Message: "Enter the instance’s base URL without credentials, query parameters, or a fragment."}
	}
	if u.Scheme == "http" && authenticated && !net.ParseIP(u.Hostname()).IsLoopback() {
		return "", &tasks.InputError{Message: "Use HTTPS to connect to an instance with a token. HTTP tokens are allowed on loopback IPs."}
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func (s *Service) verifyConnection(ctx context.Context, c tasks.ProviderCredentials, gmailAccess string) (string, string, error) {
	switch c.Source {
	case tasks.GitHubCI:
		var user struct {
			Login string `json:"login"`
			ID    int64  `json:"id"`
		}
		if err := s.getJSON(ctx, githubAPI+"/user", c.Secret, &user); err != nil {
			return "", "", errors.New("GitHub sign-in failed: " + err.Error() + ". Check the token.")
		}
		if user.Login == "" || user.ID <= 0 {
			return "", "", errors.New("GitHub did not return an account")
		}
		return user.Login, strconv.FormatInt(user.ID, 10), nil
	case tasks.GmailLabel:
		if gmailAccess == "" {
			token, problem := s.gmailToken(ctx, c)
			if problem != "" {
				return "", "", errors.New(problem)
			}
			gmailAccess = token
		}
		var user struct {
			Subject string `json:"sub"`
		}
		var profile struct {
			Email string `json:"emailAddress"`
		}
		if err := s.getJSON(ctx, "https://openidconnect.googleapis.com/v1/userinfo", gmailAccess, &user); err != nil || user.Subject == "" {
			return "", "", errors.New("Google didn’t return the account identity. Sign in again.")
		}
		if err := s.getJSON(ctx, gmailAPI+"/profile", gmailAccess, &profile); err != nil || profile.Email == "" {
			return "", "", errors.New("Couldn’t read the Gmail profile. Check that the Gmail API is enabled and read access was granted.")
		}
		return profile.Email, user.Subject, nil
	default:
		if _, err := s.monitoringAlerts(ctx, c); err != nil {
			return "", "", errors.New("Couldn’t read firing alerts: " + err.Error() + ". Check the URL and alert read access from the Buddy server.")
		}
		return "", c.Endpoint, nil
	}
}

func (s *Service) CheckConnection(ctx context.Context, id string) (tasks.ProviderConnection, error) {
	c, err := s.activeConnection(ctx, id)
	if err != nil {
		return tasks.ProviderConnection{}, err
	}
	problem := ""
	_, identity, err := s.verifyConnection(ctx, c, "")
	if err != nil {
		problem = err.Error()
	} else if identity != c.Identity {
		problem = "The credential belongs to a different account."
	}
	return s.store.NoteProviderCheck(ctx, c, problem)
}

func (s *Service) activeConnection(ctx context.Context, id string) (tasks.ProviderCredentials, error) {
	c, err := s.store.ProviderCredential(ctx, id)
	if err == nil && !c.Connected {
		err = &tasks.InputError{Message: "Reconnect this account or instance first."}
	}
	return c, err
}

func (s *Service) ConnectionScopes(ctx context.Context, id string) ([]ScopeOption, error) {
	c, err := s.activeConnection(ctx, id)
	if err != nil {
		return nil, err
	}
	switch c.Source {
	case tasks.GitHubCI:
		var repos []struct {
			Name string `json:"full_name"`
		}
		if err := s.getJSON(ctx, githubAPI+"/user/repos?per_page=100&sort=updated", c.Secret, &repos); err != nil {
			return nil, &tasks.InputError{Message: "Couldn’t list repositories: " + err.Error()}
		}
		options := make([]ScopeOption, 0, len(repos))
		for _, r := range repos {
			if r.Name != "" {
				options = append(options, ScopeOption{ID: strings.ToLower(r.Name), Name: r.Name})
			}
		}
		return options, nil
	case tasks.GmailLabel:
		token, problem := s.gmailToken(ctx, c)
		if problem != "" {
			return nil, &tasks.InputError{Message: problem}
		}
		var labels struct {
			Labels []ScopeOption `json:"labels"`
		}
		if err := s.getJSON(ctx, gmailAPI+"/labels", token, &labels); err != nil {
			return nil, &tasks.InputError{Message: "Couldn’t list Gmail labels: " + err.Error()}
		}
		return labels.Labels, nil
	default:
		return []ScopeOption{{ID: "firing", Name: "All firing alerts"}}, nil
	}
}

func (s *Service) ValidateWatchSource(ctx context.Context, w tasks.Watch) error {
	c, err := s.activeConnection(ctx, w.ConnectionID)
	if err != nil {
		return err
	}
	if c.Source != w.Source {
		return &tasks.InputError{Message: "Choose an account or instance for this type of watch."}
	}
	scope := strings.TrimSpace(w.Scope)
	switch c.Source {
	case tasks.GitHubCI:
		owner, repo, ok := strings.Cut(scope, "/")
		if !ok || owner == "" || owner == "." || owner == ".." || repo == "" || repo == "." || repo == ".." || strings.Contains(repo, "/") {
			return &tasks.InputError{Message: "Enter the GitHub repository as owner/repo."}
		}
		var runs struct {
			Runs []workflowRun `json:"workflow_runs"`
		}
		if err := s.getJSON(ctx, githubAPI+"/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/actions/runs?per_page=1", c.Secret, &runs); err != nil {
			return &tasks.InputError{Message: "Couldn’t read Actions runs for that repository. Check the repository and the token’s Actions read permission."}
		}
	case tasks.GmailLabel:
		token, problem := s.gmailToken(ctx, c)
		if problem != "" {
			return &tasks.InputError{Message: problem}
		}
		var label ScopeOption
		if err := s.getJSON(ctx, gmailAPI+"/labels/"+url.PathEscape(scope), token, &label); err != nil || label.ID != scope {
			return &tasks.InputError{Message: "Couldn’t read that Gmail label. Choose a label from this mailbox."}
		}
	}
	return nil
}
