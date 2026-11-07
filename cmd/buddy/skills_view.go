package main

import (
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"buddy/internal/api"
	"buddy/internal/tasks"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

var skillMark = svg(`<path d="M4 6h16M4 12h10M4 18h7m3 1 6-6"/>`)

func skillMatches(draft string, catalog []tasks.SkillInfo) []tasks.SkillInfo {
	prefix, ok := strings.CutPrefix(strings.TrimLeftFunc(draft, unicode.IsSpace), "/")
	if !ok || strings.ContainsRune(prefix, '/') || strings.IndexFunc(prefix, unicode.IsSpace) >= 0 {
		return nil
	}
	var matches []tasks.SkillInfo
	for _, skill := range catalog {
		if strings.HasPrefix(skill.Name, prefix) {
			matches = append(matches, skill)
		}
	}
	return matches
}

func (a *desktop) fillSkill(name string) {
	if a.selectedSkills == nil {
		a.selectedSkills = make(map[string]string)
	}
	a.selectedSkills[a.currentConversation()] = name
	a.draft = "/" + name + " "
	a.focusComposer, a.skillCaretPending = true, true
}

func (a *desktop) skillForDraft(text string) string {
	name, _ := tasks.SkillCommand(text)
	if name != "" && (a.selectedSkills[a.currentConversation()] == name ||
		slices.ContainsFunc(a.directory.Skills, func(skill tasks.SkillInfo) bool { return skill.Name == name })) {
		return name
	}
	return ""
}

func (a *desktop) skillSuggestions(c *ui.Context) {
	if !a.online || a.serverProtocol < api.SkillSelectionProtocol || a.sending {
		return
	}
	matches := skillMatches(a.draft, a.directory.Skills)
	if len(matches) == 0 {
		return
	}
	ui.Column(c.Key("skill-suggestions")).MaxWidth(510).FillWidth().Gap(2).Padding(6).
		Radius(12).Border(1, c.Theme().Border).Background(c.Theme().Surface).Children(func() {
		ui.Text(c, "Skills · Tab to complete").FontSize(11).TextColor(c.Theme().TextMuted).Padding(3, 6)
		for _, skill := range matches[:min(6, len(matches))] {
			button := ui.ButtonBase(c.Key("use-skill-"+skill.Name)).Label("/"+skill.Name).Description(skill.Description).
				FillWidth().MinHeight(29).Justify(ui.Start).Padding(3, 6).Radius(6).Gap(8)
			if button.Hovered() {
				button.Background(c.Theme().SurfaceHover)
			}
			button.OnClick(func() { a.fillSkill(skill.Name) }).Children(func() {
				ui.Text(c, "/"+skill.Name).FontSize(12).FontWeight(670).Shrink(0)
				ui.Text(c, skill.Description).FontSize(12).SingleLine().MinWidth(0).TextColor(c.Theme().TextMuted)
			})
		}
	})
}

func (a *desktop) skillsPage(c *ui.Context) {
	controlPage(c.Key("skills-page"), "Skills", "Reusable workflows for all your Buds. Type / in chat to choose one.", nil, func() {
		if !a.online || a.serverProtocol < api.SkillsProtocol {
			ui.Text(c, "Connect to an up-to-date Buddy server to use skills.").TextColor(c.Theme().TextMuted)
			return
		}
		ui.PrimaryButton(c.Key("import-skill"), "Import SKILL.md").AlignSelf(ui.Start).
			Disabled(a.skillBusy).OnClick(a.chooseSkill)
		ui.Text(c, "Import a Markdown skill to add or update its slash command on this server.").TextColor(c.Theme().TextMuted)
		if a.skillNotice != "" {
			ui.Text(c, a.skillNotice).TextColor(c.Theme().TextMuted).Selectable()
		}
		for _, skill := range a.directory.Skills {
			connectionSection(c.Key(skill.Name), func() {
				ui.Row(c).Padding(11, 13).Gap(9).Children(func() {
					connectionDetail(c, skillMark, "/"+skill.Name, skill.Description)
					if skill.BuiltIn {
						ui.Text(c, "Built in").FontSize(11).TextColor(c.Theme().TextMuted)
					} else {
						ui.Button(c.Key("remove-skill-"+skill.Name), "Remove").Disabled(a.skillBusy).
							OnClick(func() { a.removeSkill(skill.Name) })
					}
				})
			})
		}
	})
}

func (a *desktop) chooseSkill() {
	if a.window == nil || !a.online || a.skillBusy {
		return
	}
	a.skillBusy, a.skillNotice = true, ""
	win := a.window
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: win, Title: "Choose SKILL.md",
			ButtonLabel: "Import", Filters: []mygo.FileFilter{{Name: "Markdown", Extensions: []string{"md"}}}})
		var skill tasks.SkillInfo
		if err == nil && len(paths) != 0 {
			var markdown string
			markdown, err = readSkillFile(paths[0])
			if err == nil {
				skill, err = a.client.InstallSkill(a.ctx, markdown)
			}
		}
		win.Update(func() {
			a.skillBusy = false
			if err != nil {
				a.skillNotice = errorMessage(err, "Couldn’t import the skill.")
			} else if skill.Name != "" {
				a.skillNotice = "Installed /" + skill.Name
				a.refreshSoon()
			}
		})
	}()
}

func readSkillFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > tasks.MaxSkillBytes {
		return "", errors.New("Choose a Markdown file up to 32 KB.")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, tasks.MaxSkillBytes+1))
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) || len(data) > tasks.MaxSkillBytes {
		return "", errors.New("Choose a UTF-8 Markdown file up to 32 KB.")
	}
	return string(data), nil
}

func (a *desktop) removeSkill(name string) {
	if a.window == nil || !a.online || a.skillBusy {
		return
	}
	a.skillBusy, a.skillNotice = true, ""
	win := a.window
	go func() {
		err := a.client.RemoveSkill(a.ctx, name)
		win.Update(func() {
			a.skillBusy = false
			if err != nil {
				a.skillNotice = errorMessage(err, "Couldn’t remove the skill.")
				return
			}
			a.skillNotice = "Removed /" + name
			a.refreshSoon()
		})
	}()
}

func (a *desktop) budProposalCard(c *ui.Context, message tasks.Message) {
	proposal := message.BudProposal
	ui.Column(c.Key("bud-proposal")).MinWidth(0).FillWidth().Gap(7).Padding(11).
		Radius(11).Border(1, c.Theme().Border).Background(c.Theme().Surface).Children(func() {
		label := "Bud draft"
		if proposal.ConversationID != "" {
			label = "Bud created"
		}
		ui.Text(c, label).FontSize(11).TextColor(c.Theme().TextMuted)
		ui.Text(c, proposal.Name).Role(ui.RoleHeading).FontSize(15).FontWeight(670).Selectable()
		ui.Text(c, proposal.Role).FontSize(13).TextColor(c.Theme().TextMuted).Selectable()
		ui.Text(c, proposal.Instructions).FontSize(13).MinWidth(0).Selectable()
		if proposal.ConversationID != "" {
			ui.Button(c.Key("open-bud-"+message.TaskID), "Open chat").AlignSelf(ui.Start).
				OnClick(func() { a.openConversation(proposal.ConversationID) })
		} else {
			ui.PrimaryButton(c.Key("create-bud-"+message.TaskID), "Create Bud").AlignSelf(ui.Start).
				Disabled(!a.online || a.creatingProposal != "" || a.chat.Agent.Removed).OnClick(func() { a.createProposedBud(message.TaskID) })
		}
	})
}

func (a *desktop) createProposedBud(task string) {
	if a.window == nil || !a.online || a.creatingProposal != "" {
		return
	}
	a.creatingProposal, a.chatErr = task, ""
	win, chat := a.window, a.currentConversation()
	go func() {
		bud, err := a.client.CreateProposedBud(a.ctx, chat, task)
		win.Update(func() {
			a.creatingProposal = ""
			if chat == a.currentConversation() {
				if err != nil {
					a.chatErr = errorMessage(err, "Couldn’t create this Bud. Try again.")
				} else {
					for i := range a.chat.Messages {
						if message := &a.chat.Messages[i]; message.TaskID == task && message.BudProposal != nil {
							message.BudProposal.ConversationID = bud.ConversationID
							break
						}
					}
				}
			}
			a.refreshSoon()
		})
	}()
}
