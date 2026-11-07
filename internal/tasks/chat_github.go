package tasks

import (
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type GitHubChatReader interface {
	GitHubChatConnections(context.Context) ([]GitHubChatConnection, error)
	ReadGitHub(context.Context, GitHubQuery) (GitHubResult, error)
}

type GitHubChatConnection struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Account       string `json:"account"`
	Collaboration bool   `json:"collaboration,omitempty"`
}

type GitHubQuery struct {
	ConnectionID string `json:"connection_id"`
	Repo         string `json:"repo"`
	Kind         string `json:"kind"`
	Number       int    `json:"number"`
	Ref          string `json:"ref"`
}

type GitHubResult struct {
	Repo    string       `json:"repo"`
	Heading string       `json:"heading"`
	URL     string       `json:"url"`
	Detail  string       `json:"detail,omitempty"`
	Items   []GitHubItem `json:"items,omitempty"`
	Patch   string       `json:"patch,omitempty"`
	More    bool         `json:"more,omitempty"`
}

type GitHubItem struct {
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
	URL    string `json:"url,omitempty"`
}

type gitCheckout struct {
	Repo   string `json:"repo"`
	Branch string `json:"branch,omitempty"`
	Commit string `json:"commit,omitempty"`
}

func ValidGitHubRepo(name string) bool {
	_, repo, _ := strings.Cut(name, "/")
	return githubScope.MatchString(name) && repo != "." && repo != ".."
}

func gitHubOrigin(origin string) string {
	var path string
	if tail, ok := strings.CutPrefix(origin, "git@github.com:"); ok {
		path = tail
	} else if u, err := url.Parse(origin); err == nil && strings.EqualFold(u.Host, "github.com") &&
		(u.Scheme == "https" || u.Scheme == "ssh" || u.Scheme == "http" || u.Scheme == "git") {
		path = strings.TrimPrefix(u.Path, "/")
	}
	path = strings.TrimSuffix(strings.TrimSuffix(path, "/"), ".git")
	if ValidGitHubRepo(path) {
		return path
	}
	return ""
}

// Read only checkout metadata here. Worktree inspection and Git operations that
// could invoke repo-configured filters run inside the worker's sandbox.
func localGitHubCheckout(ctx context.Context, dir string, protected []string) *gitCheckout {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil
	}
	defer root.Close()
	if _, err := root.Stat(".git"); err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	read := func(args ...string) string {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull,
			"GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_NO_LAZY_FETCH=1"}
		cmd.WaitDelay = 200 * time.Millisecond
		out, err := cmd.StdoutPipe()
		if err != nil || cmd.Start() != nil {
			return ""
		}
		data, err := io.ReadAll(io.LimitReader(out, 4097))
		out.Close()
		if waitErr := cmd.Wait(); err != nil || waitErr != nil || len(data) > 4096 {
			return ""
		}
		return strings.TrimSpace(string(data))
	}
	locations := strings.Split(read("rev-parse", "--path-format=absolute", "--show-toplevel", "--absolute-git-dir", "--git-common-dir"), "\n")
	if len(locations) != 3 || filepath.Clean(locations[0]) != dir {
		return nil
	}
	for _, metadata := range locations[1:] {
		if !filepath.IsAbs(metadata) {
			return nil
		}
		for _, path := range protected {
			if path == "" {
				continue
			}
			overlap, err := WorkspacePathsOverlap(metadata, path)
			if overlap || err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil
			}
		}
	}
	repo := gitHubOrigin(read("config", "--local", "--no-includes", "--get", "remote.origin.url"))
	if repo == "" {
		return nil
	}
	return &gitCheckout{Repo: repo, Branch: read("symbolic-ref", "--short", "-q", "HEAD"), Commit: read("rev-parse", "--verify", "HEAD")}
}

func (turn Turn) completeGitHub(ctx context.Context, reply string, emit Emit, run modelPass) (string, error) {
	action := *turn.action
	*turn.action = TurnAction{}
	if action.Card != nil || action.hasRoutines() || action.BudProposal != nil || action.ReadFile != nil || action.SaveFile != nil ||
		action.ConnectionRead != nil || len(action.WatchCreate) != 0 {
		return "I can look that up on GitHub first. What would you like me to check?", nil
	}
	if turn.github == nil || len(turn.githubConnections) == 0 {
		return "Connect a GitHub account in Connections so I can look this up.", nil
	}
	query := *action.GitHubRead
	if query.Repo == "" && turn.gitCheckout != nil {
		query.Repo = turn.gitCheckout.Repo
	}
	if !ValidGitHubRepo(query.Repo) {
		return "Which GitHub repository should I check? Send its owner/repo or a GitHub link.", nil
	}
	if query.ConnectionID == "" && len(turn.githubConnections) == 1 {
		query.ConnectionID = turn.githubConnections[0].ID
	}
	validConnection := false
	for _, connection := range turn.githubConnections {
		if connection.ID == query.ConnectionID {
			validConnection = true
			break
		}
	}
	if !validConnection {
		return "Which GitHub connection should I use? Give me its name from Connections.", nil
	}
	_ = emit("activity", activityLabel(Searching, "GitHub"))
	_ = emit("commentary", "Checking GitHub for "+query.Repo+"…")
	result, err := turn.github.ReadGitHub(ctx, query)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		var invalid *InputError
		if errors.As(err, &invalid) {
			return invalid.Message, nil
		}
		return "I couldn’t reach GitHub. Try again or check the account in Connections.", nil
	}
	if action.hasHandoff() {
		// Only a handoff chosen before seeing remote text can be scheduled
		// here. The worker receives the lookup separately as reference data.
		turn.action.Handoff, turn.action.Handoffs, turn.action.githubResult = action.Handoff, action.Handoffs, &result
		return reply, nil
	}
	return turn.summarizeConnection(ctx, result, run)
}
