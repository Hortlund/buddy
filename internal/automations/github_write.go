package automations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"buddy/internal/tasks"
)

// WriteGitHub is called only by the queue after an explicit card approval.
// The revision pins the credential the user saw, even across a reconnect.
func (s *Service) WriteGitHub(ctx context.Context, p tasks.GitHubWrite, revision string) (tasks.GitHubResult, error) {
	var result tasks.GitHubResult
	if err := p.Validate(); err != nil {
		return result, err
	}
	credential, err := s.activeConnection(ctx, p.ConnectionID)
	if err != nil || credential.Source != tasks.GitHubCI || !credential.Collaboration || revision == "" ||
		credential.WriteRevision != revision || credential.Secret == "" {
		return result, &tasks.InputError{Message: "The GitHub connection changed. Ask for a fresh preview before trying again."}
	}

	body := map[string]any{"body": p.Body}
	var path, view, title string
	switch p.Kind {
	case "issue":
		path, view, title = "/issues", "/issues", "Issue created"
		body["title"] = p.Title
	case "comment":
		path = fmt.Sprintf("/issues/%d/comments", p.Number)
		view, title = fmt.Sprintf("/issues/%d", p.Number), "Comment posted"
	case "review":
		path = fmt.Sprintf("/pulls/%d/reviews", p.Number)
		view, title = fmt.Sprintf("/pull/%d", p.Number), "Review posted"
		body["event"] = "COMMENT"
	case "draft_pull":
		path, view, title = "/pulls", "/pulls", "Draft pull request opened"
		body["title"], body["head"], body["base"], body["draft"] = p.Title, p.Head, p.Base, true
	}
	data, err := json.Marshal(body)
	if err != nil {
		return result, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubAPI+"/repos/"+p.Repo+path, bytes.NewReader(data))
	if err != nil {
		return result, err
	}
	req.Header.Set("Authorization", "Bearer "+credential.Secret)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	res, err := s.client.Do(req)
	if err != nil {
		return result, &tasks.InputError{Message: tasks.GitHubWriteUncertain}
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK, http.StatusCreated:
	case http.StatusUnauthorized, http.StatusForbidden:
		return result, &tasks.InputError{Message: "GitHub refused this change. Check the account’s repository access and Issues or Pull requests write permissions."}
	case http.StatusNotFound:
		return result, &tasks.InputError{Message: "GitHub couldn’t find that repository or thread with this account. Check the target and repository access."}
	case http.StatusUnprocessableEntity:
		return result, &tasks.InputError{Message: "GitHub rejected the proposed change. For a draft PR, check that both branches exist and the head has changes."}
	case http.StatusTooManyRequests:
		return result, &tasks.InputError{Message: "GitHub’s rate limit was reached. Try again shortly."}
	default:
		return result, &tasks.InputError{Message: tasks.GitHubWriteUncertain}
	}

	web := "https://github.com/" + p.Repo
	result = tasks.GitHubResult{Repo: p.Repo, Heading: title, URL: web + view, Detail: p.Title}
	var response struct {
		URL string `json:"html_url"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&response) == nil {
		u, err := url.Parse(response.URL)
		if err == nil && u.Scheme == "https" && u.Host == "github.com" && u.User == nil &&
			strings.HasPrefix(strings.ToLower(u.Path), "/"+strings.ToLower(p.Repo)+"/") {
			result.URL = u.String()
		}
	}
	return result, nil
}
