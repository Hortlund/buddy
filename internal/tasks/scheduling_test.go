package tasks

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestScheduleRoutinesAndJobInOneTurn(t *testing.T) {
	for _, valid := range []bool{true, false} {
		t.Run(fmt.Sprintf("valid job %t", valid), func(t *testing.T) {
			ctx := t.Context()
			store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if _, _, err := store.Send(ctx, DefaultConversationID, "Make up two test routines and a background job.", ""); err != nil {
				t.Fatal(err)
			}
			task, err := store.claim(ctx)
			if err != nil {
				t.Fatal(err)
			}
			brief := "Brainstorm three project names."
			if !valid {
				brief = ""
			}
			var action TurnAction
			turn := Turn{coordinate: true, action: &action}
			reply, err := turn.decodeReply(fmt.Sprintf(`{"reply":"","routines":[
				{"name":"Daily tip","prompt":"Write a tip.","minutes":1440},
				{"name":"Weekly reading","prompt":"Suggest a book.","minutes":10080}],
				"handoffs":[{"title":"Name ideas","brief":%q,"read_only":true}]}`, brief), nil)
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
			last := chat.Messages[len(chat.Messages)-1]
			if !valid {
				if len(chat.Routines) != 0 || len(chat.Work) != 0 || !strings.Contains(last.Text, "couldn’t schedule that batch") {
					t.Fatalf("partial batch: routines=%+v work=%+v reply=%q", chat.Routines, chat.Work, last.Text)
				}
				return
			}
			if len(chat.Routines) != 2 || len(chat.Work) != 1 || chat.Work[0].Title != "Name ideas" ||
				!strings.Contains(last.Text, "Daily tip") || !strings.Contains(last.Text, "Weekly reading") || !strings.Contains(last.Text, "Name ideas") {
				t.Fatalf("incomplete batch: routines=%+v work=%+v reply=%q", chat.Routines, chat.Work, last.Text)
			}
		})
	}
}

func TestOneConfirmationForRemovingAndSchedulingWork(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(ctx, filepath.Join(t.TempDir(), "buddy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var removals []ManagementAction
	for _, name := range []string{"Old daily tip", "Old weekly review"} {
		old, err := store.AddRoutine(ctx, DefaultConversationID, Routine{Name: name, Prompt: "Post a summary.", Minutes: 1440})
		if err != nil {
			t.Fatal(err)
		}
		removals = append(removals, ManagementAction{Operation: "delete_routine", Target: old.ID})
	}
	worker := NewWorker(store, nil, nil)
	for _, test := range []struct {
		choice int
		badJob bool
	}{{choice: 1}, {choice: 0, badJob: true}, {choice: 0}} {
		if _, _, err := store.Send(ctx, DefaultConversationID, "Remove both old routines and make a replacement and a background job.", ""); err != nil {
			t.Fatal(err)
		}
		task, err := store.claim(ctx)
		if err != nil {
			t.Fatal(err)
		}
		action := &TurnAction{
			Manage:   removals,
			Routines: []RoutineDraft{{Name: "Friday digest", Prompt: "Post a weekly digest.", Minutes: 10080}},
			Handoff:  &Handoff{Title: "Draft digest format", Brief: "Draft a short format.", ReadOnly: true},
		}
		if test.badJob {
			action.Handoff.After = []int{1}
		}
		if err := store.finishTurn(ctx, task.ID, Succeeded, "Replied.", "", action); err != nil {
			t.Fatal(err)
		}
		chat, err := store.Conversation(ctx, DefaultConversationID, 0)
		if err != nil {
			t.Fatal(err)
		}
		card := chat.Messages[len(chat.Messages)-1].Card
		if len(chat.Routines) != 2 || len(chat.Work) != 0 || card == nil || card.Confirmation != "destructive" ||
			!strings.Contains(card.Detail, "Old weekly review") || !strings.Contains(card.Detail, "Friday digest") ||
			!strings.Contains(card.Detail, "Draft digest format") {
			t.Fatalf("wrong preview or early changes: %+v", card)
		}
		answer, created, err := store.AnswerCard(ctx, DefaultConversationID, task.ID, test.choice)
		if err != nil || !created {
			t.Fatalf("answer: %+v, %v, %v", answer, created, err)
		}
		run, err := store.claim(ctx)
		if err != nil || run.ID != answer.ID {
			t.Fatalf("claimed wrong confirmation: %+v, %v", run, err)
		}
		action = &TurnAction{}
		reply, err := worker.execute(ctx, run, &workerRun{}, func(string, string) error { return nil }, action)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.finishTurn(ctx, run.ID, Succeeded, "Replied.", reply, action); err != nil {
			t.Fatal(err)
		}
		chat, err = store.Conversation(ctx, DefaultConversationID, 0)
		if err != nil {
			t.Fatal(err)
		}
		if test.choice == 1 || test.badJob {
			if len(chat.Routines) != 2 || chat.Routines[0].ID != removals[1].Target ||
				chat.Routines[1].ID != removals[0].Target || len(chat.Work) != 0 || action.scheduledWork {
				t.Fatal("a cancelled or invalid batch changed the schedule")
			}
			continue
		}
		if len(chat.Routines) != 1 || chat.Routines[0].Name != "Friday digest" || len(chat.Work) != 1 ||
			chat.Work[0].Title != "Draft digest format" || !action.scheduledWork {
			t.Fatalf("incomplete confirmed batch: routines=%+v work=%+v", chat.Routines, chat.Work)
		}
	}
}
