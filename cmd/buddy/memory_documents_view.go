package main

import (
	"context"

	"buddy/internal/api"

	"github.com/egoist/mygo/ui"
)

func (a *desktop) memoryDocumentTabs(c *ui.Context, removed bool) {
	v := &a.autonomy
	panel := v.memoryPanel
	if panel == "" {
		panel = "Saved notes"
	}
	ui.Row(c).FillWidth().Gap(3).BorderWidth(0, 0, 1, 0).BorderColor(c.Theme().Border).Children(func() {
		for _, doc := range []struct{ id, label string }{
			{"Saved notes", "Saved notes"}, {"SOUL.md", "Soul"}, {"MEMORY.md", "Learned memory"},
		} {
			controlTab(c.Key(doc.id), doc.label, nil, doc.id == panel, func() {
				v.memoryPanel, v.clearingMemory = doc.id, false
			})
		}
	})
	switch {
	case panel == "Saved notes":
		return
	case v.docs.Revision == "":
		ui.Text(c, "Loading this Bud’s documents…").TextColor(c.Theme().TextMuted)
	case panel == "SOUL.md":
		a.soulDocumentView(c, removed)
	default:
		a.learnedMemoryView(c, removed)
	}
}

func (a *desktop) soulDocumentView(c *ui.Context, removed bool) {
	v := &a.autonomy
	controlSection(c, "SOUL.md", "Shape how this Bud speaks and works, or ask it to propose a change in its direct chat.", nil)
	connectionSection(c, func() {
		ui.Column(c).FillWidth().Padding(12).Gap(9).Children(func() {
			ui.TextArea(c.Key("soul-editor"), &v.soulDraft).Height(184).Label("Soul Markdown").Placeholder("# Soul")
			ui.Row(c).FillWidth().Gap(7).AlignItems(ui.Center).Children(func() {
				stale := v.soulRevision != v.docs.Revision
				if stale {
					ui.Text(c, "SOUL.md changed on the server.").Grow(1).FontSize(12).TextColor(c.Theme().TextMuted)
					ui.Button(c.Key("reload-soul"), "Reload").OnClick(func() {
						v.soulDraft, v.soulRevision = v.docs.Soul, v.docs.Revision
					})
				} else {
					ui.Box(c).Grow(1)
				}
				ui.PrimaryButton(c.Key("save-soul"), "Save changes").
					Disabled(removed || v.busy || stale || v.soulDraft == v.docs.Soul).OnClick(func() {
					bud, body, revision := v.agent, v.soulDraft, v.soulRevision
					a.changeAutonomy(func(ctx context.Context, client *api.Client) error {
						return client.SaveSoul(ctx, bud, body, revision)
					}, func() {
						if v.agent == bud && v.soulDraft == body {
							v.soulRevision = ""
						}
					})
				})
			})
		})
	})
}

func (a *desktop) learnedMemoryView(c *ui.Context, removed bool) {
	v := &a.autonomy
	controlSection(c, "MEMORY.md", "Notes this Bud learns from your private text chats.", func() {
		ui.MenuButton(c.Key("memory-actions"), "Manage", func(menu *ui.Menu) {
			label := "Pause learning"
			if !v.docs.Learning {
				label = "Resume learning"
			}
			if menu.Item(label).Disabled(v.busy || removed).Chosen() {
				bud, enabled := v.agent, !v.docs.Learning
				a.changeAutonomy(func(ctx context.Context, client *api.Client) error {
					return client.SetBudLearning(ctx, bud, enabled)
				}, nil)
			}
			menu.Separator()
			if menu.Item("Clear learned notes…").Disabled(v.busy || v.docs.Memory == "").Chosen() {
				v.clearingMemory = true
			}
		}).Label("Manage learned memory")
	})
	if !v.docs.Learning {
		ui.Text(c, "Learning is paused. Existing notes are still available to this Bud.").FontSize(12).TextColor(c.Theme().TextMuted)
	}
	if v.clearingMemory {
		connectionSection(c, func() {
			ui.Row(c).FillWidth().Padding(12).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "Clear this Bud’s learned notes?").Grow(1)
				quietControl(c.Key("keep-memory"), "Keep notes", nil, !v.busy, func() { v.clearingMemory = false })
				ui.Button(c.Key("clear-memory-confirm"), "Clear notes").TextColor(c.Theme().Danger).
					Disabled(v.busy).OnClick(func() {
					bud := v.agent
					a.changeAutonomy(func(ctx context.Context, client *api.Client) error {
						return client.ClearLearnedMemory(ctx, bud)
					}, func() { v.clearingMemory = false })
				})
			})
		})
	}
	if v.docs.Memory == "" {
		controlEmpty(c, fileMark, "No learned notes yet", "Useful preferences and project details will appear here as you chat.")
		return
	}
	connectionSection(c, func() {
		ui.Column(c).FillWidth().Padding(14).Children(func() { messageMarkdown(c.Key("learned-notes"), v.docs.Memory) })
	})
}
