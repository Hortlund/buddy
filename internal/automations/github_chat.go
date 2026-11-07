package automations

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"buddy/internal/tasks"
)

const githubPreviewRows = 6

var gitCommit = regexp.MustCompile(`^(?:[a-fA-F0-9]{40}|[a-fA-F0-9]{64})$`)

func (s *Service) GitHubChatConnections(ctx context.Context) ([]tasks.GitHubChatConnection, error) {
	connections, err := s.store.ProviderConnections(ctx)
	if err != nil {
		return nil, err
	}
	var result []tasks.GitHubChatConnection
	for _, c := range connections {
		if c.Source == tasks.GitHubCI && c.Connected {
			result = append(result, tasks.GitHubChatConnection{ID: c.ID, Name: c.Name, Account: c.Account, Collaboration: c.Collaboration})
		}
	}
	return result, nil
}

type githubThread struct {
	Number       int                       `json:"number"`
	Title        string                    `json:"title"`
	State        string                    `json:"state"`
	Body         string                    `json:"body"`
	PullRequest  *struct{}                 `json:"pull_request"`
	User         struct{ Login string }    `json:"user"`
	Draft        bool                      `json:"draft"`
	Merged       bool                      `json:"merged"`
	ChangedFiles int                       `json:"changed_files"`
	Head         struct{ Ref, SHA string } `json:"head"`
	Base         struct{ Ref string }      `json:"base"`
}

func (t githubThread) item(repoURL, kind string) tasks.GitHubItem {
	state := cleanHeading(t.State, 32)
	switch {
	case t.Merged:
		state = "merged"
	case t.Draft:
		state = "draft"
	}
	if login := cleanHeading(t.User.Login, 80); login != "" {
		state += " · @" + login
	}
	return tasks.GitHubItem{
		Title:  fmt.Sprintf("#%d %s", t.Number, cleanHeading(t.Title, 220)),
		Detail: state,
		URL:    fmt.Sprintf("%s/%s/%d", repoURL, kind, t.Number),
	}
}

func (s *Service) ReadGitHub(ctx context.Context, query tasks.GitHubQuery) (tasks.GitHubResult, error) {
	if !tasks.ValidGitHubRepo(query.Repo) || len(query.Ref) > 250 ||
		((query.Kind == "pull" || query.Kind == "issue") && query.Number < 1) {
		return tasks.GitHubResult{}, &tasks.InputError{Message: "Give me the repository and the PR or issue number you want me to check."}
	}
	c, err := s.activeConnection(ctx, query.ConnectionID)
	if err != nil || c.Source != tasks.GitHubCI || c.Secret == "" {
		return tasks.GitHubResult{}, &tasks.InputError{Message: "That GitHub connection is unavailable. Check it in Connections."}
	}
	base := githubAPI + "/repos/" + query.Repo
	web := "https://github.com/" + query.Repo
	get := func(path string, out any) error {
		err := s.getJSON(ctx, base+path, c.Secret, out)
		switch {
		case errors.Is(err, responseError(http.StatusNotFound)):
			return &tasks.InputError{Message: "I couldn’t find that on GitHub. Check the repository or number and the connection’s repository access."}
		case errors.Is(err, responseError(http.StatusUnauthorized)), errors.Is(err, responseError(http.StatusForbidden)):
			return &tasks.InputError{Message: "GitHub refused the lookup. Check the connection’s token, read permissions, and rate limit."}
		case errors.Is(err, responseError(http.StatusTooManyRequests)):
			return &tasks.InputError{Message: "GitHub’s rate limit was reached. Try again shortly."}
		}
		return err
	}
	result := tasks.GitHubResult{Repo: query.Repo, URL: web}
	switch query.Kind {
	case "repo":
		var repo struct {
			Description   string `json:"description"`
			DefaultBranch string `json:"default_branch"`
			Private       bool   `json:"private"`
		}
		if err := get("", &repo); err != nil {
			return result, err
		}
		result.Heading = "Repository"
		result.Detail = cleanHeading(repo.Description, 400)
		visibility := "Public"
		if repo.Private {
			visibility = "Private"
		}
		result.Items = []tasks.GitHubItem{{Title: "Default branch", Detail: cleanHeading(repo.DefaultBranch, 160) + " · " + visibility}}
	case "pulls", "issues":
		path, itemPath, page := "pulls", "pull", githubPreviewRows+1
		result.Heading = "Open pull requests"
		if query.Kind == "issues" {
			path, itemPath, page = "issues", "issues", 30
			result.Heading = "Open issues"
		}
		var entries []githubThread
		if err := get(fmt.Sprintf("/%s?state=open&per_page=%d", path, page), &entries); err != nil {
			return result, err
		}
		result.URL += "/" + path
		result.More = len(entries) >= page
		for _, entry := range entries {
			if entry.Number < 1 || entry.PullRequest != nil {
				continue
			}
			if len(result.Items) == githubPreviewRows {
				result.More = true
				break
			}
			result.Items = append(result.Items, entry.item(web, itemPath))
		}
		if len(result.Items) == 0 {
			result.Detail = "No open items in this preview."
		}
	case "pull", "issue":
		path, itemPath := "pulls", "pull"
		if query.Kind == "issue" {
			path, itemPath = "issues", "issues"
		}
		endpoint := fmt.Sprintf("/%s/%d", path, query.Number)
		var entry githubThread
		if err := get(endpoint, &entry); err != nil {
			return result, err
		}
		if entry.Number != query.Number || (query.Kind == "issue" && entry.PullRequest != nil) {
			return result, &tasks.InputError{Message: "That number isn’t an issue in this repository. Ask for the pull request if that’s what you meant."}
		}
		item := entry.item(web, itemPath)
		result.Heading, result.URL, result.Detail = item.Title, item.URL, item.Detail
		if query.Kind == "pull" {
			result.Detail += fmt.Sprintf("\n\n%s → %s · %d changed files",
				cleanHeading(entry.Head.Ref, 130), cleanHeading(entry.Base.Ref, 130), entry.ChangedFiles)
			if gitCommit.MatchString(entry.Head.SHA) {
				result.Detail += " · " + entry.Head.SHA[:12]
			}
			s.githubPatchPreview(get, endpoint, entry.ChangedFiles, &result)
		}
		if body := clipGitHubText(entry.Body, 1800); body != "" {
			result.Detail += "\n\n" + body
		}
	case "checks":
		result.Heading, result.URL = "Recent workflow runs", web+"/actions"
		params := url.Values{"per_page": {"7"}}
		if query.Ref != "" {
			if gitCommit.MatchString(query.Ref) {
				params.Set("head_sha", query.Ref)
			} else {
				params.Set("branch", query.Ref)
			}
			result.Detail = "Ref " + cleanHeading(query.Ref, 180)
		}
		var runs struct {
			Total int `json:"total_count"`
			Items []struct {
				workflowRun
				Status string `json:"status"`
			} `json:"workflow_runs"`
		}
		if err := get("/actions/runs?"+params.Encode(), &runs); err != nil {
			return result, err
		}
		for _, run := range runs.Items[:min(len(runs.Items), githubPreviewRows)] {
			if run.ID < 1 {
				continue
			}
			state := run.Status
			if run.Conclusion != "" {
				state = run.Conclusion
			}
			result.Items = append(result.Items, tasks.GitHubItem{
				Title: cleanHeading(run.Name, 180), Detail: strings.ReplaceAll(cleanHeading(state, 40), "_", " ") + " · " + cleanHeading(run.Branch, 140),
				URL: fmt.Sprintf("%s/actions/runs/%d", web, run.ID),
			})
		}
		result.More = runs.Total > githubPreviewRows || len(runs.Items) > githubPreviewRows
		if len(result.Items) == 0 {
			result.Items = []tasks.GitHubItem{{Title: "No workflow runs found"}}
		}
	default:
		return result, &tasks.InputError{Message: "I can look up a repository, pull requests, issues, or recent Actions runs."}
	}
	return result, nil
}

func (s *Service) githubPatchPreview(get func(string, any) error, endpoint string, changed int, result *tasks.GitHubResult) {
	var files []struct {
		Name      string `json:"filename"`
		Status    string `json:"status"`
		Patch     string `json:"patch"`
		Additions int    `json:"additions"`
		Deletions int    `json:"deletions"`
	}
	if err := get(endpoint+"/files?per_page=6", &files); err != nil {
		result.Detail += "\n\nThe changed-file preview is unavailable."
		return
	}
	result.More = changed > len(files)
	var patch strings.Builder
	for _, file := range files[:min(len(files), githubPreviewRows)] {
		name := cleanHeading(file.Name, 220)
		result.Items = append(result.Items, tasks.GitHubItem{Title: name,
			Detail: fmt.Sprintf("%s · +%d −%d", cleanHeading(file.Status, 40), file.Additions, file.Deletions)})
		if file.Patch == "" || patch.Len() >= 3600 {
			continue
		}
		patch.WriteString(fmt.Sprintf("--- a/%s\n+++ b/%s\n%s\n", name, name, clipGitHubText(file.Patch, 3600-patch.Len())))
	}
	result.Patch = strings.TrimRight(patch.String(), "\n")
}

func clipGitHubText(text string, limit int) string {
	text = strings.ReplaceAll(strings.TrimSpace(text), "\x00", "")
	if len(text) <= limit {
		return text
	}
	return strings.ToValidUTF8(text[:limit], "") + "\n…"
}
