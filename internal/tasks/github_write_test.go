package tasks

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitHubCardAppliesExactApprovedProposalOnce(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	connection, err := store.SaveProviderConnection(ctx, ProviderCredentials{
		ProviderConnection: ProviderConnection{Source: GitHubCI, Name: "Work", Account: "octocat", Collaboration: true},
		Identity:           "42", Secret: "test-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	credential, err := store.ProviderCredential(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	worker := NewWorker(store, nil, nil)
	calls := 0
	worker.ApplyGitHubWrite = func(_ context.Context, proposal GitHubWrite, revision string) (GitHubResult, error) {
		calls++
		if proposal.ConnectionID != connection.ID || proposal.Kind != "comment" || proposal.Repo != "org/project" ||
			proposal.Number != 7 || proposal.Body != "The tests passed." || revision != credential.WriteRevision {
			t.Fatalf("unexpected GitHub proposal: %+v", proposal)
		}
		return GitHubResult{Heading: "Comment posted", URL: "https://github.com/org/project/issues/7#issuecomment-8"}, nil
	}
	emit := func(string, string) error { return nil }
	for _, choice := range []int{1, 0} {
		if _, _, err := store.Send(ctx, DefaultConversationID, "Post the test update to issue #7.", ""); err != nil {
			t.Fatal(err)
		}
		task, err := store.claim(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var action TurnAction
		turn := Turn{coordinate: true, action: &action,
			githubConnections: []GitHubChatConnection{{ID: connection.ID, Collaboration: true}}}
		reply, err := turn.complete(ctx, "", "", nil, emit, func(string) (string, error) {
			return `{"reply":"","github_write":{"connection_id":"","repo":"org/project","kind":"comment","number":7,"body":"The tests passed."}}`, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.finishTurn(ctx, task.ID, Succeeded, "Replied.", reply, &action); err != nil {
			t.Fatal(err)
		}
		chat, err := store.Conversation(ctx, DefaultConversationID, 0)
		if err != nil {
			t.Fatal(err)
		}
		preview := chat.Messages[len(chat.Messages)-1].Card
		if preview == nil || preview.Confirmation != "github" || !strings.Contains(preview.Detail, "The tests passed.") ||
			!strings.Contains(preview.Detail, "org/project · Work (octocat)\nThread #7") || calls != 0 {
			t.Fatalf("unexpected preview: %+v, provider calls=%d", preview, calls)
		}
		answer, created, err := store.AnswerCard(ctx, DefaultConversationID, task.ID, choice)
		if err != nil || !created {
			t.Fatalf("answer: %+v, %v, %v", answer, created, err)
		}
		if repeated, created, err := store.AnswerCard(ctx, DefaultConversationID, task.ID, choice); err != nil || created || repeated.ID != answer.ID {
			t.Fatalf("repeated answer: %+v, %v, %v", repeated, created, err)
		}
		claimed, err := store.claim(ctx)
		if err != nil || claimed.ID != answer.ID {
			t.Fatalf("claim: %+v, %v", claimed, err)
		}
		action = TurnAction{}
		reply, err = worker.execute(ctx, claimed, &workerRun{}, emit, &action)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.finishTurn(ctx, claimed.ID, Succeeded, "Replied.", reply, &action); err != nil {
			t.Fatal(err)
		}
		chat, err = store.Conversation(ctx, DefaultConversationID, 0)
		if err != nil {
			t.Fatal(err)
		}
		last := chat.Messages[len(chat.Messages)-1]
		if choice == 0 && (calls != 1 || last.GitHub == nil) || choice == 1 && (calls != 0 || last.Text != "GitHub change cancelled.") {
			t.Fatalf("unexpected result: %+v, provider calls=%d", last, calls)
		}
	}
}
