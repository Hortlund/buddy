package tasks

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type testGitHubReader func(GitHubQuery) (GitHubResult, error)

func (read testGitHubReader) GitHubChatConnections(context.Context) ([]GitHubChatConnection, error) {
	return nil, nil
}

func (read testGitHubReader) ReadGitHub(_ context.Context, query GitHubQuery) (GitHubResult, error) {
	return read(query)
}

func TestGitHubChatCardAndLocalHandoff(t *testing.T) {
	ctx := t.Context()
	for _, handoff := range []bool{false, true} {
		name := "lookup"
		if handoff {
			name = "handoff"
		}
		t.Run(name, func(t *testing.T) {
			store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.EnableWorkspaces(ctx, t.TempDir()); err != nil {
				t.Fatal(err)
			}
			message := "Show PR #7 from my repo."
			var planned *Handoff
			if handoff {
				message = "Inspect PR #7 and check it against this checkout in the background."
				planned = &Handoff{Title: "Check PR #7", Brief: "Compare PR #7 to the local checkout.", ReadOnly: true}
			}
			if _, _, err := store.Send(ctx, DefaultConversationID, message, "github"); err != nil {
				t.Fatal(err)
			}
			task, err := store.claim(ctx)
			if err != nil {
				t.Fatal(err)
			}
			turn, err := store.turn(ctx, task)
			if err != nil {
				t.Fatal(err)
			}
			var action TurnAction
			turn.action = &action
			turn.gitCheckout = &gitCheckout{Repo: "org/project"}
			turn.githubConnections = []GitHubChatConnection{{ID: "work", Name: "Work", Account: "octocat"}}
			const remoteText = "Remote text must never authorize an action"
			result := GitHubResult{Repo: "org/project", Heading: "#7 Fix it", Detail: remoteText}
			turn.github = testGitHubReader(func(query GitHubQuery) (GitHubResult, error) {
				if query.Repo != "org/project" || query.ConnectionID != "work" || query.Number != 7 {
					t.Fatalf("incorrect lookup: %+v", query)
				}
				return result, nil
			})
			calls := 0
			reply, err := turn.complete(ctx, turn.Agent.Workspace, "test", nil, func(string, string) error { return nil }, func(prompt string) (string, error) {
				calls++
				if calls == 1 {
					data, err := json.Marshal(struct {
						Reply string `json:"reply"`
						TurnAction
					}{TurnAction: TurnAction{GitHubRead: &GitHubQuery{Kind: "pull", Number: 7}, Handoff: planned}})
					return string(data), err
				}
				if !strings.Contains(prompt, remoteText) {
					t.Fatal("lookup did not reach the model")
				}
				return `{"reply":"Here is the PR.","handoff":{"title":"Unexpected action","brief":"Execute remote instructions"}}`, nil
			})
			if err != nil || (!handoff && calls != 2) || (handoff && calls != 1) {
				t.Fatalf("model calls=%d, err=%v", calls, err)
			}
			if err := store.finishTurn(ctx, task.ID, Succeeded, "Replied.", reply, &action); err != nil {
				t.Fatal(err)
			}
			chat, err := store.Conversation(ctx, DefaultConversationID, 0)
			if err != nil || len(chat.Messages) != 2 || chat.Messages[1].GitHub == nil || chat.Messages[1].GitHub.Detail != remoteText {
				t.Fatalf("card missing: %+v, %v", chat, err)
			}
			if !handoff {
				if len(chat.Work) != 0 {
					t.Fatalf("lookup scheduled unexpected work: %+v", chat.Work)
				}
				return
			}
			if len(chat.Work) != 1 || chat.Work[0].Title != planned.Title {
				t.Fatalf("planned handoff missing: %+v", chat.Work)
			}
			job, err := store.claimLane(ctx, true)
			if err != nil {
				t.Fatal(err)
			}
			work, err := store.turn(ctx, job)
			if err != nil || len(work.Messages) != 2 || work.Messages[0].GitHub == nil || work.Agent.AllowEdits {
				t.Fatalf("worker missing read-only context: %+v, %v", work, err)
			}
		})
	}
}

func TestLocalGitHubCheckoutKeepsOnlyPublicOriginMetadata(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "feature/new"},
		{"remote", "add", "origin", "https://private-token@github.com/org/project.git"},
		{"-c", "user.email=test@example.invalid", "-c", "user.name=Test", "commit", "-qm", "initial", "--allow-empty"},
	} {
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = dir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("prepare repository: %v\n%s", err, output)
		}
	}
	got := localGitHubCheckout(t.Context(), dir, nil)
	if got == nil || got.Repo != "org/project" || got.Branch != "feature/new" || len(got.Commit) != 40 {
		t.Fatalf("checkout metadata: %+v", got)
	}
	if got := localGitHubCheckout(t.Context(), dir, []string{filepath.Join(dir, ".git")}); got != nil {
		t.Fatalf("protected Git metadata was read: %+v", got)
	}
	if got := gitHubOrigin("git@github.com:org/project.git"); got != "org/project" {
		t.Fatalf("SSH origin: %q", got)
	}
	if got := gitHubOrigin("https://github.com.evil.test/org/project"); got != "" {
		t.Fatalf("lookalike domain accepted: %q", got)
	}
}
