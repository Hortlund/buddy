package api

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"buddy/internal/localconfig"
	"buddy/internal/tasks"
)

func TestChatDecidesWhenToUseTools(t *testing.T) {
	ctx, stop := context.WithTimeout(t.Context(), 8*time.Second)
	defer stop()
	dir := t.TempDir()
	token, err := testToken(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := tasks.OpenStore(ctx, filepath.Join(dir, "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	release := make(chan struct{})
	runner := func(ctx context.Context, turn tasks.Turn, emit tasks.Emit) (string, error) {
		last := turn.Messages[len(turn.Messages)-1].Text
		if last == "hello" {
			return "Hey there.", nil
		}
		if len(turn.Messages) != 3 || turn.Messages[1].Text != "Hey there." ||
			turn.Agent.Name != "Scout" || turn.Agent.Instructions != "Work carefully." {
			return "", fmt.Errorf("runner did not get conversation or agent context")
		}
		if err := emit("activity", "Reading the repository…"); err != nil {
			return "", err
		}
		select {
		case <-release:
			return "Found the tests.", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	worker := tasks.NewWorker(store, tasks.RunRepositoryCheck, runner)
	jobCtx, stopWorker := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); worker.Run(jobCtx) }()
	defer func() { stopWorker(); <-done }()
	server := startTestServer(t, dir, Handler(token, store, worker, nil))
	defer server.Close()
	client := NewClient(server.URL, filepath.Join(dir, localconfig.TokenName))
	defer client.Close()
	id := tasks.DefaultConversationID

	if _, err := client.Send(ctx, id, "hello", "hello-id"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ctx, func() bool {
		conv, err := client.Conversation(ctx, id, 0)
		return err == nil && len(conv.Messages) == 2 && conv.Messages[1].Text == "Hey there." && !conv.Messages[1].DidWork
	})
	if work, err := client.List(ctx); err != nil || len(work) != 0 {
		t.Fatalf("plain chat created a work item: %+v, %v", work, err)
	}

	agent := tasks.Agent{ID: tasks.DefaultAgentID, Name: "Scout", Instructions: "Work carefully."}
	if _, err := client.SaveAgent(ctx, agent); err != nil {
		t.Fatal(err)
	}
	submission, disconnect := context.WithCancel(ctx)
	task, err := client.Send(submission, id, "Where are the tests?", "inspect-id")
	disconnect()
	if err != nil {
		t.Fatal(err)
	}
	client.Close()

	other := NewClient(server.URL, filepath.Join(dir, localconfig.TokenName))
	defer other.Close()
	waitFor(t, ctx, func() bool {
		conv, err := other.Conversation(ctx, id, 0)
		if err != nil || len(conv.Messages) != 3 {
			return false
		}
		m := conv.Messages[2]
		return m.DidWork && m.Status == tasks.Running && m.Activity == "Reading the repository…"
	})
	close(release)
	waitFor(t, ctx, func() bool {
		conv, err := other.Conversation(ctx, id, 0)
		if err != nil || len(conv.Messages) != 4 {
			return false
		}
		m := conv.Messages[3]
		return m.TaskID == task.ID && m.Role == "assistant" && m.Text == "Found the tests." &&
			m.Status == tasks.Succeeded && m.DidWork && conv.Agent.Name == "Scout"
	})
	if work, err := other.List(ctx); err != nil || len(work) != 1 || work[0].ID != task.ID {
		t.Fatalf("work list = %+v, %v", work, err)
	}
}

func TestJobInputResumesInTheSameChat(t *testing.T) {
	ctx, stop := context.WithTimeout(t.Context(), 10*time.Second)
	defer stop()
	dir := t.TempDir()
	token, err := testToken(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := tasks.OpenStore(ctx, filepath.Join(dir, "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const question = "Which repository should I inspect?"
	worker := tasks.NewWorker(store, nil, func(ctx context.Context, turn tasks.Turn, emit tasks.Emit) (string, error) {
		if !strings.Contains(turn.Brief, "Use the API repository") {
			return question, tasks.ErrWorkNeedsInput
		}
		if len(turn.Attachments) != 1 || turn.Attachments[0].Name != "repo.txt" {
			return "", fmt.Errorf("the job did not receive its input attachment")
		}
		return "I inspected the API repository.", nil
	})
	jobCtx, stopWorker := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); worker.Run(jobCtx) }()
	defer func() { stopWorker(); <-done }()
	server := startTestServer(t, dir, Handler(token, store, worker, nil))
	defer server.Close()
	client := NewClient(server.URL, filepath.Join(dir, localconfig.TokenName))
	defer client.Close()
	id := tasks.DefaultConversationID
	job, err := store.QueueVoiceWork(ctx, id, tasks.DefaultAgentID, "call", "action", "Inspect repo", "Inspect the requested repository.")
	if err != nil {
		t.Fatal(err)
	}
	worker.WakeBackground()
	var input string
	waitFor(t, ctx, func() bool {
		detail, err := client.WorkDetails(ctx, id, job.ID)
		input = detail.InputID
		return err == nil && input != "" && detail.Status == tasks.NeedsInput && detail.FinishedAt == nil &&
			detail.Question == question && detail.Result == ""
	})
	files := []tasks.Attachment{{Name: "repo.txt", Data: []byte("api")}}
	if err := client.AnswerWorkInput(ctx, id, input, "Use the API repository", "answer", files); err != nil {
		t.Fatal(err)
	}
	if err := client.AnswerWorkInput(ctx, id, input, "Use the API repository", "answer", files); err != nil {
		t.Fatalf("retry of the same answer failed: %v", err)
	}
	waitFor(t, ctx, func() bool {
		detail, err := client.WorkDetails(ctx, id, job.ID)
		return err == nil && detail.Status == tasks.Succeeded && detail.InputID == "" &&
			detail.Result == "I inspected the API repository."
	})
	chat, err := client.Conversation(ctx, id, 0)
	if err != nil || len(chat.Work) != 1 || len(chat.Messages) != 3 {
		t.Fatalf("work and transcript: %+v, %v", chat, err)
	}
	if i := slices.IndexFunc(chat.Messages, func(m tasks.Message) bool {
		return m.Role == "user" && m.TaskID == input && m.JobID == job.ID
	}); i != 1 || chat.Messages[0].Status == tasks.NeedsInput || chat.Messages[0].Text != question {
		t.Fatalf("question and linked answer: %+v", chat.Messages)
	}
}
