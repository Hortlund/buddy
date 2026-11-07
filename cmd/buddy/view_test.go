package main

import (
	"slices"
	"testing"
	"time"

	"buddy/internal/api"
	"buddy/internal/tasks"

	"github.com/egoist/mygo/ui"
)

func TestRemovedBudChatIsAvailableAsHistory(t *testing.T) {
	a := &desktop{online: true, section: "buds", serverProtocol: api.RemoveBudProtocol}
	a.directory.Agents = []tasks.AgentRef{
		{ID: "aster", Name: "Aster", ConversationID: "aster-chat"},
		{ID: "birch", Name: "Birch", ConversationID: "birch-chat"},
	}
	a.directory.RemovedAgents = []tasks.AgentRef{{ID: "finch", Name: "Finch", ConversationID: "finch-chat"}}
	view := ui.NewTester(a.view, 1120, 750)
	if _, ok := view.Find("Remove · Aster"); !ok {
		t.Fatal("Bud settings did not expose removal")
	}
	if err := view.Click("Removed Buds"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("Open Finch’s chat"); err != nil {
		t.Fatal(err)
	}
	if a.currentConversation() != "finch-chat" {
		t.Fatalf("opened the wrong conversation: %q", a.currentConversation())
	}
	a.chat.Agent = tasks.Agent{ID: "finch", Name: "Finch", Removed: true}
	a.chat.Messages = []tasks.Message{{ID: 1, Role: "user", Text: "My earlier message"}}
	view.Frame()
	if _, ok := view.Find("Message"); ok || !view.HasText("This Bud was removed.") || !view.HasText("My earlier message") {
		t.Fatalf("removed Bud chat should display only history: %v", view.Texts())
	}
}

func TestRemovedBudMemoryDraftKeepsItsOriginalTarget(t *testing.T) {
	a := &desktop{online: true}
	a.directory.PrimaryAgentID = "aster"
	a.directory.Agents = []tasks.AgentRef{{ID: "aster", Name: "Aster"}}
	a.directory.RemovedAgents = []tasks.AgentRef{{ID: "finch", Name: "Finch"}}
	note := tasks.MemoryNote{Note: "Use Finch’s repo.", Source: "Workspace"}
	a.autonomy = autonomyView{tab: "Memory", agent: "finch", memory: note, expiry: "2027-01-01", editingMemory: true}
	a.showAutonomy()
	if a.autonomy.agent != "finch" || a.autonomy.memory != note || a.autonomy.expiry != "2027-01-01" {
		t.Fatalf("draft moved away from its original Bud: %+v", a.autonomy)
	}
	view := ui.NewTester(a.autonomyMemory, 760, 450)
	if !view.HasText("This Bud was removed. Copy your draft before choosing another Bud.") {
		t.Fatal("the removed target was not disclosed")
	}
	if err := view.Click("Choose a Bud for memory"); err != nil {
		t.Fatal(err)
	}
	if err := view.ChooseMenuItem("Aster"); err != nil {
		t.Fatal(err)
	}
	if a.autonomy.agent != "aster" || a.autonomy.memory.Note != "" || a.autonomy.expiry != "" {
		t.Fatalf("a new target inherited the old draft: %+v", a.autonomy)
	}
}

func TestGroupHistoryKeepsRemovedSpeakerNameAndAppearance(t *testing.T) {
	a := &desktop{}
	a.chat.Name = "Project"
	a.chat.Agent = tasks.Agent{ID: "aster", Name: "Aster", Avatar: &tasks.Avatar{Color: "ink"}}
	a.chat.Members = []tasks.AgentRef{{ID: "aster", Name: "Aster"}}
	a.directory.RemovedAgents = []tasks.AgentRef{{ID: "finch", Name: "Finch", Avatar: &tasks.Avatar{Color: "rose"}}}
	message := tasks.Message{TaskID: "reply", Role: "assistant", AgentID: "finch", AgentName: "Finch", Text: "The tests are ready."}
	view := ui.NewTester(func(c *ui.Context) { a.reply(c, message, nil) }, 480, 140)
	if !view.HasText("Finch") {
		t.Fatalf("one-member group hid the earlier speaker: %v", view.Texts())
	}
	if profile := a.replyProfile(message.AgentID); profile.ID != "finch" || profile.Avatar == nil || profile.Avatar.Color != "rose" {
		t.Fatalf("historical reply used the wrong avatar: %+v", profile)
	}
}

func TestGitHubCardOpensDiffAndSource(t *testing.T) {
	a := &desktop{}
	const link = "https://github.com/example/repo/pull/7"
	message := tasks.Message{TaskID: "reply", Role: "assistant", GitHub: &tasks.GitHubResult{
		Repo: "example/repo", Heading: "#7 Update server", URL: link,
		Items: []tasks.GitHubItem{{Title: "main.go", Detail: "modified · +1 −1"}},
		Patch: "--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new",
	}}
	view := ui.NewTester(func(c *ui.Context) { a.reply(c, message, nil) }, 600, 400)
	if !view.HasText("#7 Update server") || view.HasText("Git diff") {
		t.Fatalf("expected a collapsed GitHub card, got %v", view.Texts())
	}
	if err := view.Click("Show diff preview"); err != nil {
		t.Fatal(err)
	}
	if !view.HasText("Git diff") || !view.HasText("Hide diff preview") {
		t.Fatalf("diff didn't expand: %v", view.Texts())
	}
	if err := view.Click("Open on GitHub"); err != nil {
		t.Fatal(err)
	}
	if opened := view.OpenedURLs(); len(opened) != 1 || opened[0] != link {
		t.Fatalf("opened unexpected URL: %v", opened)
	}
}

func TestConnectionCardsShowEvidenceAndWatchControls(t *testing.T) {
	a := &desktop{}
	const dashboard = "https://grafana.example.com/d/node"
	card := tasks.ConnectionCard{Source: tasks.GrafanaAlerts, Connection: "Production", Heading: "Dashboards",
		Items: []tasks.ConnectionItem{{Title: "Node overview", Detail: "Ops", URL: dashboard}}}
	view := ui.NewTester(func(c *ui.Context) { a.connectionCard(c, &card, "task") }, 620, 350)
	if !view.HasText("Grafana · Production") || !view.HasText("Node overview") {
		t.Fatalf("dashboard card missing: %v", view.Texts())
	}
	if err := view.Click("Open"); err != nil {
		t.Fatal(err)
	}
	if opened := view.OpenedURLs(); len(opened) != 1 || opened[0] != dashboard {
		t.Fatalf("opened unexpected URL: %v", opened)
	}
	card = tasks.ConnectionCard{Source: tasks.PrometheusAlerts, Heading: "Instant query", Warnings: []string{"Partial data"},
		Items: []tasks.ConnectionItem{{Title: "up", Detail: "job=api", Value: "1"}}}
	view.Frame()
	if !view.HasText("1") || !view.HasText("job=api") || !view.HasText("Partial data") {
		t.Fatalf("metric result missing: %v", view.Texts())
	}
	card = tasks.ConnectionCard{Heading: "Watch ready to activate", WatchDraft: true}
	view.Frame()
	if !view.HasText("Activate watch") {
		t.Fatal("watch draft is missing its activation control")
	}
	card = tasks.ConnectionCard{Heading: "Watch created", WatchID: "watch_test",
		Items: []tasks.ConnectionItem{{Title: "Main CI", Detail: "GitHub · Work · org/repo · branch main", Badge: "Notify in chat"}}}
	view.Frame()
	if err := view.Click("Manage watches"); err != nil {
		t.Fatal(err)
	}
	if a.section != "autonomy" || a.autonomy.tab != "Watches" {
		t.Fatal("watch card did not open Autonomy")
	}
}

func TestDestructiveConfirmationShowsSpecificActions(t *testing.T) {
	a := &desktop{online: true}
	card := &tasks.ChoiceCard{Confirmation: "destructive", Question: "Delete the routine and stop the job?",
		Detail: "Delete routine “Daily”\nStop job “Check builds”", Options: []string{"Delete and stop", "Cancel"}}
	view := ui.NewTester(func(c *ui.Context) { a.choiceCard(c, tasks.Message{TaskID: "confirm", Card: card}) }, 420, 180)
	if _, ok := view.Find("Delete and stop"); !ok {
		t.Fatal("missing the named destructive action")
	}
	if _, ok := view.Find("Cancel"); !ok || view.HasText("Your choice") || !view.HasText("Check builds") {
		t.Fatalf("confirmation did not show direct actions and affected items: %v", view.Texts())
	}
	cancel := 1
	card.Choice = &cancel
	view.Frame()
	if _, ok := view.Find("Delete and stop"); ok || !view.HasText("Cancelled") {
		t.Fatalf("resolved confirmation still shows actionable buttons: %v", view.Texts())
	}
}

func TestArchivedWorkOpensHistoryWithoutFollowUp(t *testing.T) {
	a := &desktop{online: true}
	work := workBrowser{selected: tasks.WorkItem{
		ID: "job", Title: "Previous work", ConversationID: "old-group", ChatArchived: true,
	}}
	view := ui.NewTester(func(c *ui.Context) { a.workDetail(c, &work, false) }, 720, 430)
	if _, ok := view.Find("Ask about this job"); ok {
		t.Fatal("archived work still offered a chat follow-up")
	}
	if err := view.Click("Open chat"); err != nil {
		t.Fatal(err)
	}
	if a.currentConversation() != "old-group" {
		t.Fatal("archived work could not reopen its chat history")
	}
}

func TestRemovingInspectedBudFallsBackToTheSelectedRecipient(t *testing.T) {
	chat := tasks.Conversation{
		Name: "Project", Agent: tasks.Agent{ID: "lead"},
		Members: []tasks.AgentRef{{ID: "lead"}, {ID: "recipient"}},
	}
	inspected, workspace := resolveChatSelection(chat, "removed")
	if inspected != "" || workspace != "lead" {
		t.Fatalf("invalid inspector fallback: inspected=%q workspace=%q", inspected, workspace)
	}
}

func TestComposerDoesNotTransferInputAcrossRuns(t *testing.T) {
	for _, label := range []string{"Stop", "Steer now"} {
		t.Run(label, func(t *testing.T) {
			a := &desktop{online: true, draft: "correction"}
			a.chat.Active = &tasks.ActiveRun{ID: "first", Status: tasks.Running, CanSteer: true}
			view := ui.NewTester(a.composer, 760, 300)
			bounds, ok := view.Find(label)
			if !ok {
				t.Fatalf("could not find %q", label)
			}
			x, y := bounds.X+bounds.W/2, bounds.Y+bounds.H/2
			view.Press(x, y)
			if !view.Focused(label) {
				t.Fatalf("%q did not receive the initial press", label)
			}
			a.chat.Active = &tasks.ActiveRun{ID: "second", Status: tasks.Running, CanSteer: true}
			view.Frame()
			if view.Focused(label) {
				t.Fatalf("%q kept the previous run's input state", label)
			}
			view.Release(x, y)
		})
	}
}

func TestActivityBubbleShowsOnlyMeaningfulText(t *testing.T) {
	a := &desktop{}
	a.chat.Agent.Name = "Scout"
	active := &tasks.ActiveRun{ID: "run", Status: tasks.Running, Kind: tasks.Listening, Activity: "Waiting for the model…"}
	view := ui.NewTester(func(c *ui.Context) { a.reply(c, tasks.Message{}, active) }, 450, 100)
	if view.HasText(active.Activity) || view.HasText(a.chat.Agent.Name) {
		t.Fatalf("initial bubble showed a status or name: %v", view.Texts())
	}
	active.Kind, active.Activity = tasks.Reading, "Looking through files · worker.go"
	view.Frame()
	if !view.HasText(active.Activity) {
		t.Fatalf("concrete tool activity was missing: %v", view.Texts())
	}
	active.Update = "I found where the worker saves replies."
	view.Frame()
	if !view.HasText(active.Update) || view.HasText(active.Activity) || view.HasText(a.chat.Agent.Name) {
		t.Fatalf("public update shared the bubble with other labels: %v", view.Texts())
	}
	active.Kind, active.Activity, active.Update = tasks.Replying, "Writing a reply…", ""
	view.Frame()
	if view.HasText(active.Activity) {
		t.Fatalf("reply filler appeared: %v", view.Texts())
	}
}

func TestReplyKeepsItsRowAndComposerSlot(t *testing.T) {
	a := &desktop{online: true}
	a.chat.Messages = []tasks.Message{{ID: 1, TaskID: "turn", Role: "user", Text: "Hey Buddy"}}
	a.chat.Active = &tasks.ActiveRun{ID: "turn", Status: tasks.Running, Kind: tasks.Listening}
	view := ui.NewTester(a.view, 1120, 750)
	stop, ok := view.Find("Stop")
	if !ok {
		t.Fatal("working composer is missing Stop")
	}
	workingKey := a.list.Key(1)
	a.chat.Active = nil
	a.chat.Messages = append(a.chat.Messages, tasks.Message{ID: 2, TaskID: "turn", Role: "assistant", Text: "Hey there!"})
	view.Frame()
	send, ok := view.Find("Send")
	if !ok || send != stop {
		t.Fatalf("composer button moved from %+v to %+v", stop, send)
	}
	if completedKey := a.list.Key(1); completedKey != workingKey || !view.HasText("Hey there!") {
		t.Fatalf("reply did not replace its working row: %v to %v", workingKey, completedKey)
	}
}

func TestCompactHeaderAndInlineComposer(t *testing.T) {
	a := &desktop{online: true}
	a.chat.Agent = tasks.Agent{Name: "Scout", Model: tasks.DefaultCodexModel}
	view := ui.NewTester(a.view, 820, 590)
	view.SetTitleBar(ui.TitleBar{Height: 42, Left: 80, Right: 140})
	view.Frame()
	brand, brandFound := view.Find("Buddy")
	chat, chatFound := view.Find("Scout")
	workspace, workspaceFound := view.Find("Workspace")
	if !brandFound || !chatFound || !workspaceFound || brand.X >= 24 || brand.Y < 42 || workspace.X+workspace.W > 820-140 {
		t.Fatalf("header overlapped the window controls: %v, %v, %v", brand, chat, workspace)
	}
	if chat.Y > 22 || workspace.Y > 22 {
		t.Fatalf("headers dropped below the title bar: %v, %v, %v", brand, chat, workspace)
	}
	input, inputFound := view.Find("Message")
	send, sendFound := view.Find("Send")
	if !inputFound || !sendFound || input.X+input.W > send.X || input.Y+input.H/2 != send.Y+send.H/2 {
		t.Fatalf("send button was not inline: %v, %v", input, send)
	}
}

func TestDesktopPagesAlignAtEveryWindowSize(t *testing.T) {
	for _, size := range []struct {
		name          string
		width, height int
		rightInset    float32
	}{
		{"minimum", 820, 590, 120},
		{"default", 1120, 750, 0},
		{"wide", 1400, 850, 0},
	} {
		t.Run(size.name, func(t *testing.T) {
			a := &desktop{online: true, serverProtocol: api.DesktopProtocol, editReturn: "buds",
				edit: tasks.Agent{ID: "scout", Name: "Scout", Avatar: &tasks.Avatar{}}}
			a.chat.Agent = tasks.Agent{ID: "scout", Name: "Scout"}
			view := ui.NewTester(a.view, size.width, size.height)
			view.SetTitleBar(ui.TitleBar{Height: 66, Left: 82, Right: size.rightInset})
			find := func(label string) ui.Rect {
				t.Helper()
				bounds, ok := view.Find(label)
				if !ok {
					t.Fatalf("%s: missing %q", a.section, label)
				}
				return bounds
			}

			a.section = "menu"
			view.Frame()
			wantHeading := find("Review activity and manage your Buddy workspace.")
			wantBack := find("Back to chat")
			menuRow := find("Work")
			if wantBack.X < 186 || wantBack.X-186 > 24 ||
				menuRow.X != wantHeading.X+1 || float32(size.width)-(menuRow.X+menuRow.W) != menuRow.X-186 {
				t.Fatalf("menu did not fill the main pane with even gutters: heading %v, back %v, row %v", wantHeading, wantBack, menuRow)
			}
			for _, page := range []struct {
				section, subtitle, back string
			}{
				{"buds", "Manage your Buds and choose which one Buddy opens first.", "Back to menu"},
				{"agent", "Update this Bud’s profile, instructions, and workspace.", "Back to Buds"},
				{"skills", "Reusable workflows for all your Buds. Type / in chat to choose one.", "Back to menu"},
				{"work", "Follow what your Buds are doing and revisit finished jobs.", "Back to menu"},
				{"preferences", "Choose how Buddy looks and which languages it uses.", "Back to menu"},
				{"autonomy", "Review what your Buds do and remember between chats.", "Back to menu"},
				{"connections", "Choose where Buddy lives and what it can use.", "Back to menu"},
				{"history", "Open or restore an archived group. Its routines and watches stay paused until you resume them.", "Back to menu"},
				{"group", "Choose the Buds who can share this conversation and hand off work.", "Back to menu"},
				{"routine", "Runs with Scout and posts results in this chat.", "Back to chat"},
			} {
				a.section = page.section
				view.Frame()
				heading, back := find(page.subtitle), find(page.back)
				if heading.X != wantHeading.X || heading.Y != wantHeading.Y || back.X != wantBack.X || back.Y != wantBack.Y {
					t.Errorf("%s: heading %v and back %v did not align with %v and %v", page.section, heading, back, wantHeading, wantBack)
				}
				if page.back == "Back to chat" {
					continue
				}
				chat := find("Return to chat")
				gutter := float32(size.width) - chat.X - chat.W
				if delta := gutter - max(16, size.rightInset); delta < -.5 || delta > .5 || chat.Y != wantBack.Y {
					t.Errorf("%s: Return to chat was not pinned to the right edge: %v", page.section, chat)
				}
			}
		})
	}
}

func TestGroupAndCallRowsExposeContextualArchive(t *testing.T) {
	a := &desktop{online: true, serverProtocol: api.ConversationHistoryProtocol}
	group := ui.NewTester(func(c *ui.Context) {
		a.chatNav(c, "group", "Project", groupMark, false)
	}, 260, 100)
	if err := group.RightClick("Project"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(group.Menu(), "Archive group chat") || !slices.Contains(group.Menu(), "Call history…") || a.conversationID != "" {
		t.Fatalf("right-click did not open the group's actions: %v", group.Menu())
	}
	when := time.Date(2026, time.November, 6, 21, 12, 13, 0, time.UTC)
	a.history = historyView{chat: "group", calls: []tasks.CallHistory{{ID: "call", StartedAt: when}}}
	calls := ui.NewTester(a.historyPage, 640, 450)
	if err := calls.RightClick(when.Local().Format("Jan 2, 2006 · 15:04:05")); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(calls.Menu(), "Archive call") {
		t.Fatalf("saved call context menu: %v", calls.Menu())
	}
}
