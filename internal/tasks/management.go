package tasks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

const managementSchema = `
CREATE TABLE IF NOT EXISTS management_cards (
	task_id TEXT PRIMARY KEY REFERENCES choice_cards(task_id),
	choices TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS management_confirmations (
	task_id TEXT PRIMARY KEY REFERENCES management_cards(task_id),
	style TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS management_schedules (
	task_id TEXT PRIMARY KEY REFERENCES management_cards(task_id),
	batch TEXT NOT NULL
);
`

// ManagementAction names a server operation and a concrete Buddy object. It
// never contains an endpoint, shell command, credential, or filesystem path.
type ManagementAction struct {
	Operation string   `json:"operation"`
	Target    string   `json:"target_id"`
	Text      string   `json:"text"`
	Members   []string `json:"members"`
	Revision  string   `json:"revision,omitempty"`
	Evidence  string   `json:"evidence,omitempty"`
}

func (a *TurnAction) hasManagement() bool {
	return a != nil && (len(a.Manage) != 0 || len(a.ManagementChoices) != 0 || a.managementConfirmed)
}

type managementTarget struct {
	ManagementAction
	label, kind, detail string
}

func managementScope(ctx context.Context, db queryer, task string) (chat, bud string, err error) {
	err = db.QueryRowContext(ctx, `SELECT ct.conversation_id,COALESCE(td.agent_id,c.agent_id)
		FROM chat_turns ct JOIN conversations c ON c.id=ct.conversation_id
		LEFT JOIN turn_details td ON td.task_id=ct.task_id WHERE ct.task_id=?`, task).Scan(&chat, &bud)
	return
}

func managementTargets(ctx context.Context, tx *sql.Tx, chat, bud string, plan []ManagementAction) ([]managementTarget, error) {
	if len(plan) == 0 || len(plan) > 8 {
		return nil, &InputError{"Choose up to eight specific changes at a time."}
	}
	targets := make([]managementTarget, 0, len(plan))
	seen := make(map[string]bool)
	for _, action := range plan {
		action.Target = strings.TrimSpace(action.Target)
		action.Text = strings.TrimSpace(action.Text)
		t := managementTarget{ManagementAction: action}
		var row *sql.Row
		switch t.Operation {
		case "stop_job":
			t.kind = "job"
			row = tx.QueryRowContext(ctx, `SELECT td.title,ct.agent_name||' · '||
				CASE t.status WHEN 'needs_input' THEN 'waiting for your reply' WHEN 'cancelling' THEN 'stopping' ELSE t.status END
				||' · '||datetime(t.created/1000000,'unixepoch')||' UTC'
				FROM turn_details td JOIN chat_turns ct ON ct.task_id=td.task_id
				JOIN tasks t ON t.id=td.task_id WHERE ct.conversation_id=? AND t.id=? AND t.kind=?`, chat, t.Target, BackgroundWork)
		case "pause_routine", "resume_routine", "delete_routine":
			t.kind = "routine"
			row = tx.QueryRowContext(ctx, `SELECT r.name,a.name||' · '||CASE r.enabled WHEN 1 THEN 'active' ELSE 'paused' END
				||' · '||CASE r.minutes WHEN 60 THEN 'hourly' WHEN 1440 THEN 'daily' WHEN 10080 THEN 'weekly' ELSE 'every '||r.minutes||' min' END
				FROM routines r JOIN agents a ON a.id=r.agent_id WHERE r.conversation_id=? AND r.id=?`, chat, t.Target)
		case "pause_watch", "resume_watch", "archive_watch":
			t.kind = "watch"
			row = tx.QueryRowContext(ctx, "SELECT name,scope FROM standing_watches WHERE conversation=? AND id=? AND enabled>=0", chat, t.Target)
		case "set_primary_bud", "set_group_lead":
			t.kind = "Bud"
			row = tx.QueryRowContext(ctx, `SELECT name,'' FROM agents a WHERE a.id=? AND NOT EXISTS(
				SELECT 1 FROM removed_buds r WHERE r.agent_id=a.id) AND (? OR EXISTS(
				SELECT 1 FROM group_members g WHERE g.conversation_id=? AND g.agent_id=a.id))`, t.Target, t.Operation == "set_primary_bud", chat)
		case "forget_memory", "update_memory":
			t.kind = "memory"
			row = tx.QueryRowContext(ctx, "SELECT note,'' FROM standing_memory WHERE bud=? AND id=?", bud, t.Target)
		case "forget_learned_memory", "update_learned_memory":
			t.kind = "learned memory"
			row = tx.QueryRowContext(ctx, "SELECT note,'' FROM bud_learned_memory WHERE agent_id=? AND topic=?", bud, t.Target)
		case "edit_soul":
			t.kind, t.label = "SOUL.md", t.Text
			var private bool
			if err := tx.QueryRowContext(ctx, "SELECT NOT EXISTS(SELECT 1 FROM group_chats WHERE conversation_id=?)", chat).Scan(&private); err != nil {
				return nil, err
			}
			if !private {
				return nil, &InputError{"Ask the Bud in its direct chat to edit SOUL.md."}
			}
			if len(t.Text) > 760 || t.Text == "" {
				return nil, &InputError{"Keep chat edits to SOUL.md under 760 bytes so you can review the whole change. Use the Memory editor for longer files."}
			}
			_, revision, err := soulDocument(ctx, tx, bud)
			if err != nil {
				return nil, err
			}
			if t.Revision != "" && t.Revision != revision {
				return nil, &InputError{"SOUL.md changed after this card was created. Ask for the edit again."}
			}
			t.Revision = revision
		case "remember":
			t.kind, t.label = "memory", t.Text
		case "create_group":
			t.kind, t.label = "group chat", t.Text
			if err := validateGroupRequest(GroupRequest{Name: t.Text, Members: t.Members, LeadAgentID: t.Target}); err != nil {
				return nil, err
			}
			var members []string
			if t.Target != "" && !slices.Contains(t.Members, t.Target) {
				return nil, &InputError{"Choose a Lead Bud from the group members."}
			}
			for i, member := range t.Members {
				var name string
				err := tx.QueryRowContext(ctx, `SELECT name FROM agents a WHERE a.id=? AND NOT EXISTS(
					SELECT 1 FROM removed_buds r WHERE r.agent_id=a.id)`, member).Scan(&name)
				if errors.Is(err, sql.ErrNoRows) || slices.Contains(t.Members[:i], member) {
					return nil, &InputError{"Select distinct Buds from your workspace."}
				}
				if err != nil {
					return nil, err
				}
				if member == t.Target {
					name += " (lead)"
				}
				members = append(members, name)
			}
			t.detail = "Buds: " + strings.Join(members, ", ")
		default:
			return nil, &InputError{"That Buddy action is unavailable here."}
		}
		if row != nil {
			if err := row.Scan(&t.label, &t.detail); errors.Is(err, sql.ErrNoRows) {
				return nil, &InputError{"I couldn’t find that " + t.kind + " in the allowed scope."}
			} else if err != nil {
				return nil, err
			}
		}
		if t.savesMemory() {
			if t.Text == "" || len(t.Text) > 800 || strings.ContainsRune(t.Text, 0) {
				return nil, &InputError{"Give me a memory under 800 bytes."}
			}
			if t.Operation == "update_learned_memory" && !validTitle(t.Text, 400) {
				return nil, &InputError{"Keep the learned note to one line under 400 bytes."}
			}
		}
		if t.Operation == "forget_memory" || t.Operation == "forget_learned_memory" {
			revision := documentRevision(t.label)
			if t.Revision != "" && t.Revision != revision {
				return nil, &InputError{"That memory changed. Ask me to forget it again so you can review the current note."}
			}
			t.Revision = revision
		}
		key := t.kind + "/" + t.Target
		if row == nil {
			key += "/" + t.label
		}
		if seen[key] {
			return nil, &InputError{"Choose one change per item."}
		}
		seen[key] = true
		targets = append(targets, t)
	}
	return targets, nil
}

func (t managementTarget) preview() string {
	switch t.Operation {
	case "set_primary_bud":
		return "Make " + t.label + " the Primary Bud"
	case "set_group_lead":
		return "Make " + t.label + " the lead of this group"
	case "create_group":
		return "Create group “" + t.label + "”"
	case "edit_soul":
		return "Replace SOUL.md with:\n" + t.Text
	case "remember":
		return "Remember “" + t.label + "”"
	case "forget_memory", "forget_learned_memory":
		return "Forget “" + t.label + "”"
	default:
		verb, _, _ := strings.Cut(t.Operation, "_")
		return strings.ToUpper(verb[:1]) + verb[1:] + " " + t.kind + " “" + t.label + "”"
	}
}

func (t managementTarget) destructive() bool {
	switch t.Operation {
	case "delete_routine", "archive_watch", "forget_memory", "forget_learned_memory":
		return true
	default:
		return false
	}
}

func managementConfirmation(targets []managementTarget) (question, label, style string) {
	style = "standard"
	if slices.ContainsFunc(targets, managementTarget.destructive) {
		style = "destructive"
	}
	if len(targets) == 1 {
		t := targets[0]
		switch t.Operation {
		case "delete_routine":
			label = "Delete routine"
		case "forget_memory", "forget_learned_memory":
			label = "Forget memory"
		case "archive_watch":
			label = "Archive watch"
		case "edit_soul":
			return "Update SOUL.md?", "Save SOUL.md", style
		case "stop_job":
			label = "Stop job"
		case "pause_routine":
			label = "Pause routine"
		case "resume_routine":
			label = "Resume routine"
		case "pause_watch":
			label = "Pause watch"
		case "resume_watch":
			label = "Resume watch"
		case "set_primary_bud", "set_group_lead":
			label = "Confirm"
		case "create_group":
			label = "Create group"
		default:
			label = "Confirm"
		}
		return managementExcerpt(t.preview(), 170) + "?", label, style
	}
	if len(targets) == 2 && slices.ContainsFunc(targets, func(t managementTarget) bool { return t.Operation == "delete_routine" }) &&
		slices.ContainsFunc(targets, func(t managementTarget) bool { return t.Operation == "stop_job" }) {
		return "Delete the routine and stop the job?", "Delete and stop", style
	}
	if !slices.ContainsFunc(targets, func(t managementTarget) bool { return t.Operation != "delete_routine" }) {
		return fmt.Sprintf("Delete these %d routines?", len(targets)), "Delete routines", style
	}
	if style == "destructive" {
		return "Remove these items?", "Confirm removal", style
	}
	return "Confirm these changes?", "Confirm", style
}

func saveManagementCard(ctx context.Context, tx *sql.Tx, task, chat, bud string, plans [][]ManagementAction, batch *turnSchedule) error {
	if len(plans) == 0 || len(plans) > 4 {
		return &InputError{"Offer one to four specific choices."}
	}
	card := ChoiceCard{Question: "What would you like me to change?"}
	savedPlans := make([][]ManagementAction, 0, len(plans)+1)
	for i, plan := range plans {
		targets, err := managementTargets(ctx, tx, chat, bud, plan)
		if err != nil {
			return err
		}
		if len(plans) > 1 && slices.ContainsFunc(targets, managementTarget.destructive) {
			card.Question = "Which items should I remove?"
		}
		var changes []string
		var saved []ManagementAction
		for _, target := range targets {
			saved = append(saved, target.ManagementAction)
			detail := target.preview()
			if target.detail != "" && (len(plans) != 1 || len(targets) != 1) {
				detail += " (" + target.detail + ")"
			}
			changes = append(changes, detail)
		}
		savedPlans = append(savedPlans, saved)
		label := managementExcerpt(targets[0].preview(), 90)
		if targets[0].Operation == "edit_soul" {
			label = "Save SOUL.md"
		}
		detail := strings.Join(changes, " · ")
		if len(plans) == 1 {
			card.Question, label, card.Confirmation = managementConfirmation(targets)
			if batch != nil {
				card.Question, label = "Schedule and apply these changes?", "Apply changes"
				if card.Confirmation == "destructive" {
					card.Question = "Schedule work and remove these items?"
				}
				card.Detail = strings.Join(append(changes, batch.preview()...), "\n")
			} else if len(targets) > 1 || targets[0].Operation == "edit_soul" || card.Question != targets[0].preview()+"?" {
				card.Detail = strings.Join(changes, "\n")
			} else {
				card.Detail = targets[0].detail
			}
			detail = ""
		} else if len(changes) == 2 && !slices.Contains(card.Options, "Both") {
			label = "Both"
		} else if len(changes) > 1 || slices.Contains(card.Options, label) {
			label = fmt.Sprintf("Option %d", i+1)
		}
		if len(detail) > 280 || len(card.Detail) > confirmationDetailLimit {
			return &InputError{"Split these changes into smaller requests so I can show every change on the card."}
		}
		card.Options = append(card.Options, label)
		card.OptionDetails = append(card.OptionDetails, detail)
	}
	card.Options = append(card.Options, "Cancel")
	card.OptionDetails = append(card.OptionDetails, "")
	encoded, err := json.Marshal(append(savedPlans, nil))
	if err != nil {
		return err
	}
	if err := saveChoiceCard(ctx, tx, task, card); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO management_cards VALUES (?,?)", task, string(encoded)); err != nil {
		return err
	}
	if card.Confirmation != "" {
		if _, err = tx.ExecContext(ctx, "INSERT INTO management_confirmations VALUES (?,?)", task, card.Confirmation); err != nil {
			return err
		}
	}
	if batch != nil {
		encoded, err := json.Marshal(batch)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO management_schedules VALUES (?,?)", task, string(encoded))
		return err
	}
	return nil
}

func managementExcerpt(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) <= limit {
		return text
	}
	cut := limit - len("…")
	for cut > 0 && text[cut]&0xc0 == 0x80 {
		cut--
	}
	return text[:cut] + "…"
}

// Immediate changes are limited to one clearly named item and a command
// visible in the authenticated user's own turn. Everything else gets a card
// with the exact server operations bound to each answer.
func normalizeManagementRequest(request string) string {
	request = strings.TrimSpace(request)
	if strings.HasPrefix(request, "\"") || strings.HasPrefix(request, "“") || strings.HasPrefix(request, "`") {
		return ""
	}
	request = strings.ToLower(strings.Join(strings.Fields(strings.TrimRight(request, " .!?")), " "))
	for _, prefix := range []string{"can you ", "could you ", "would you ", "kan du ", "please ", "snälla "} {
		request = strings.TrimPrefix(request, prefix)
	}
	return strings.TrimSuffix(strings.TrimSuffix(request, " please"), " tack")
}

func explicitManagementRequest(request string, t managementTarget) bool {
	var verbs []string
	switch t.Operation {
	case "stop_job":
		verbs = []string{"stop", "cancel", "avbryt", "stoppa"}
	case "pause_routine", "pause_watch":
		verbs = []string{"pause", "disable", "pausa", "inaktivera"}
	case "resume_routine":
		verbs = []string{"resume", "enable", "återuppta", "aktivera"}
	default:
		return false
	}
	// Keep punctuation within names and reject extra sentences or conditions.
	request = normalizeManagementRequest(request)
	kinds := map[string][]string{"job": {"job", "task", "jobb", "jobbet"}, "routine": {"routine", "rutin", "rutinen"}, "watch": {"watch", "bevakning", "bevakningen"}}
	name := strings.ToLower(strings.Join(strings.Fields(t.label), " "))
	for _, verb := range verbs {
		for _, noun := range kinds[t.kind] {
			for _, target := range []string{noun + " " + name, name + " " + noun, noun + " named " + name, noun + " “" + name + "”", noun + " \"" + name + "\""} {
				if request == verb+" "+target || request == verb+" the "+target {
					return true
				}
			}
		}
	}
	return false
}

func applyManagement(ctx context.Context, tx *sql.Tx, chat, bud string, targets []managementTarget, action *TurnAction) (string, error) {
	var replies, stopped []string
	for _, t := range targets {
		label := "“" + escapeTitle(t.label) + "”"
		var note string
		var err error
		switch t.Operation {
		case "stop_job":
			var job Task
			job, err = cancelTask(ctx, tx, t.Target, false)
			switch {
			case errors.Is(err, ErrInactive):
				note, err = "The job "+label+" is already "+string(job.Status)+".", nil
			case err == nil && job.Status == Cancelling:
				note = "The job " + label + " is stopping."
				stopped = append(stopped, t.Target)
			default:
				note = "Stopped the job " + label + "."
			}
		case "pause_routine", "resume_routine", "delete_routine":
			var enabled *bool
			if t.Operation != "delete_routine" {
				value := t.Operation == "resume_routine"
				enabled = &value
			}
			err = updateRoutine(ctx, tx, chat, t.Target, enabled)
			past := map[string]string{"pause_routine": "Paused", "resume_routine": "Resumed", "delete_routine": "Deleted"}
			note = past[t.Operation] + " the routine " + label + "."
		case "pause_watch", "resume_watch", "archive_watch":
			state := map[string]int{"pause_watch": 0, "resume_watch": 1, "archive_watch": -1}
			past := map[string]string{"pause_watch": "Paused", "resume_watch": "Resumed", "archive_watch": "Archived"}
			if err = action.managementProblems[t.Target]; err == nil {
				history := action.managementWatchHistory[t.Target]
				if t.Operation == "resume_watch" && history == "" {
					var needsHistory bool
					err = tx.QueryRowContext(ctx, `SELECT enabled=0 AND source LIKE ? FROM standing_watches WHERE id=?`, GmailLabel+":%", t.Target).Scan(&needsHistory)
					if err == nil && needsHistory {
						err = &InputError{"I couldn’t get a fresh starting point from Gmail. Check the connection and try resuming again."}
					}
				}
				if err == nil {
					err = setWatchState(ctx, tx, t.Target, state[t.Operation], history)
				}
			}
			note = past[t.Operation] + " the watch " + label + "."
		case "set_primary_bud":
			err = setPrimaryAgent(ctx, tx, t.Target)
			note = "Made " + label + " the Primary Bud."
		case "set_group_lead":
			err = setGroupLead(ctx, tx, chat, t.Target)
			note = "Made " + label + " the lead of this group."
		case "create_group":
			_, err = createGroup(ctx, tx, GroupRequest{Name: t.Text, Members: t.Members, LeadAgentID: t.Target})
			note = "Created the group " + label + "."
		case "edit_soul":
			err = saveSoul(ctx, tx, bud, t.Text, t.Revision)
			note = "Updated my SOUL.md."
		case "remember":
			var existing bool
			err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM standing_memory WHERE bud=? AND note=? COLLATE NOCASE
				AND (expires=0 OR expires>?))`, bud, t.Text, time.Now().UnixMicro()).Scan(&existing)
			if err == nil && !existing {
				_, err = saveStandingMemory(ctx, tx, MemoryNote{AgentID: bud, Note: t.Text, Source: "Saved in chat"})
			}
			note = "I’ll remember that."
		case "update_memory":
			_, err = tx.ExecContext(ctx, "UPDATE standing_memory SET note=? WHERE id=? AND bud=?", t.Text, t.Target, bud)
			note = "Updated that memory."
		case "update_learned_memory":
			_, err = tx.ExecContext(ctx, "UPDATE bud_learned_memory SET note=?,updated=? WHERE agent_id=? AND topic=?",
				t.Text, time.Now().UnixMicro(), bud, t.Target)
			note = "Updated that note in my MEMORY.md."
		case "forget_memory":
			err = deleteStandingMemory(ctx, tx, t.Target)
			note = "Removed that memory."
		case "forget_learned_memory":
			result, execErr := tx.ExecContext(ctx, "DELETE FROM bud_learned_memory WHERE agent_id=? AND topic=?", bud, t.Target)
			err = requireStandingChange(result, execErr)
			note = "Removed that memory from my MEMORY.md."
		}
		if err != nil {
			return "", err
		}
		replies = append(replies, note)
	}
	action.managementStopped = stopped
	return strings.Join(replies, "\n\n"), nil
}

func saveManagementTurn(ctx context.Context, tx *sql.Tx, task string, action *TurnAction) (string, error) {
	action.managementCard, action.managementStopped = false, nil
	action.scheduledWork = false
	if action.managementConfirmed && len(action.Manage) == 0 {
		return "Cancelled.", nil
	}
	if action.Card != nil || action.ReadFile != nil ||
		action.SaveFile != nil || action.BudProposal != nil || action.GitHubRead != nil || action.ConnectionRead != nil || len(action.WatchCreate) != 0 ||
		len(action.Manage) != 0 && len(action.ManagementChoices) != 0 {
		return "I can make those Buddy changes separately. Which action should I do first?", nil
	}
	var batch *turnSchedule
	if action.hasHandoff() || action.hasRoutines() {
		if len(action.ManagementChoices) != 0 {
			return "I need to know which items you mean before making those changes together.", nil
		}
		draft, err := action.schedule(task)
		if err != nil {
			return err.Error(), nil
		}
		batch = &draft
	}
	chat, bud, err := managementScope(ctx, tx, task)
	if err != nil {
		return "", err
	}
	var request string
	err = tx.QueryRowContext(ctx, "SELECT text FROM messages WHERE task_id=? AND role='user'", task).Scan(&request)
	if errors.Is(err, sql.ErrNoRows) {
		return "Ask me in chat to make that change.", nil
	}
	if err != nil {
		return "", err
	}
	var linkedJob string
	if len(action.Manage) == 1 && action.Manage[0].Operation == "stop_job" {
		err = tx.QueryRowContext(ctx, "SELECT job_id FROM work_questions WHERE task_id=?", task).Scan(&linkedJob)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
	}
	memoryDirect := false
	if !action.managementConfirmed {
		plans := append(slices.Clone(action.ManagementChoices), action.Manage)
		for _, plan := range plans {
			if !slices.ContainsFunc(plan, func(a ManagementAction) bool {
				return a.savesMemory() || a.Operation == "forget_learned_memory"
			}) {
				continue
			}
			privateBud, message, err := privateMemoryTurn(ctx, tx, task, false)
			if err != nil {
				return "", err
			}
			if privateBud != bud {
				return "Tell me what to remember or forget in a plain message in our private chat.", nil
			}
			saves := slices.ContainsFunc(plan, ManagementAction.savesMemory)
			if saves && (len(action.ManagementChoices) != 0 || slices.ContainsFunc(plan, func(a ManagementAction) bool { return !a.savesMemory() })) {
				return "Tell me what to remember separately from those other changes.", nil
			}
			for _, change := range plan {
				if change.savesMemory() && !memoryEvidence(message, change.Evidence) {
					return "Tell me exactly what you’d like me to remember.", nil
				}
			}
			memoryDirect = memoryDirect || saves
		}
	}
	note, err := planManagement(ctx, tx, chat, bud, request, linkedJob, memoryDirect, action, func(plans [][]ManagementAction) error {
		return saveManagementCard(ctx, tx, task, chat, bud, plans, batch)
	})
	if err != nil {
		var invalid *InputError
		switch {
		case errors.As(err, &invalid):
			return "I couldn’t apply those changes. " + invalid.Message, nil
		case errors.Is(err, ErrNotFound), errors.Is(err, ErrInactive):
			return "That item has changed since I last checked. No changes were applied.", nil
		case errors.Is(err, ErrQueueFull):
			return "The background work queue is full. No changes were applied.", nil
		}
	}
	return note, err
}

func (a ManagementAction) savesMemory() bool {
	return a.Operation == "remember" || a.Operation == "update_memory" || a.Operation == "update_learned_memory"
}

func planManagement(ctx context.Context, tx *sql.Tx, chat, bud, request, linkedJob string, memoryDirect bool, action *TurnAction, offer func([][]ManagementAction) error) (string, error) {
	plans := action.ManagementChoices
	if len(plans) != 0 && len(action.Manage) != 0 {
		return "", &InputError{"Choose changes or offer choices, but don’t send both."}
	}
	if len(plans) == 0 {
		targets, err := managementTargets(ctx, tx, chat, bud, action.Manage)
		if err != nil {
			return "", err
		}
		direct := memoryDirect
		if !action.managementConfirmed && len(targets) == 1 && !targets[0].destructive() {
			target := targets[0]
			switch target.Operation {
			case "pause_routine", "resume_routine", "pause_watch":
				// Interpret natural phrasing in the model and verify it came from
				// the latest user turn. A duplicate name still needs a choice.
				if userTurnEvidence(request, target.Evidence) || explicitManagementRequest(request, target) {
					direct, err = uniqueManagementName(ctx, tx, chat, target)
				}
			case "stop_job":
				if explicitManagementRequest(request, target) {
					direct, err = uniqueManagementName(ctx, tx, chat, target)
				}
			}
			if err != nil {
				return "", err
			}
		}
		if linkedJob != "" && len(targets) == 1 && targets[0].Target == linkedJob &&
			slices.Contains([]string{"stop this job", "cancel this job", "stop it", "cancel it", "avbryt jobbet", "stoppa jobbet"}, normalizeManagementRequest(request)) {
			direct = true
		}
		if action.managementConfirmed || direct && !action.hasHandoff() && !action.hasRoutines() {
			if _, err := tx.ExecContext(ctx, "SAVEPOINT apply_management"); err != nil {
				return "", err
			}
			note, applyErr := applyManagement(ctx, tx, chat, bud, targets, action)
			if applyErr == nil && action.approvedSchedule != nil {
				var scheduled string
				scheduled, applyErr = action.approvedSchedule.apply(ctx, tx)
				if applyErr == nil {
					note += "\n\n" + scheduled
					action.scheduledWork = len(action.approvedSchedule.Handoffs) != 0
				}
			}
			if applyErr != nil {
				action.managementStopped = nil
				if _, err := tx.ExecContext(ctx, "ROLLBACK TO apply_management"); err != nil {
					return "", err
				}
			}
			_, err = tx.ExecContext(ctx, "RELEASE apply_management")
			return note, errors.Join(applyErr, err)
		}
		plans = [][]ManagementAction{action.Manage}
	}
	if err := offer(plans); err != nil {
		return "", err
	}
	action.managementCard = true
	return "", nil
}

// ManageVoice shares the chat action allowlist. A change needing a choice is
// posted as a native card so the caller can apply it in the call's chat.
func (w *Worker) ManageVoice(ctx context.Context, chat, bud, call, request string, plan []ManagementAction, choices [][]ManagementAction) (string, error) {
	if strings.TrimSpace(request) == "" {
		return "", &InputError{"Ask for the change in this call first."}
	}
	tx, err := w.store.beginMobileWrite(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	agent, err := conversationParticipant(ctx, tx, chat, bud)
	if err != nil {
		return "", err
	}
	action := &TurnAction{Manage: plan, ManagementChoices: choices}
	note, err := planManagement(ctx, tx, chat, bud, request, "", false, action, func(plans [][]ManagementAction) error {
		task, err := newVoiceLine(ctx, tx, chat, call, agent)
		if err != nil {
			return err
		}
		if err := saveManagementCard(ctx, tx, task.ID, chat, bud, plans, nil); err != nil {
			return err
		}
		return addReply(ctx, tx, task.ID, "")
	})
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	if len(action.managementStopped) != 0 {
		w.signalManagedStops(action.managementStopped)
	}
	if action.managementCard {
		return "I’ll need your confirmation. Choose an option in this call’s chat.", nil
	}
	return note, nil
}

func (s *Store) confirmedManagement(ctx context.Context, task string) ([]ManagementAction, *turnSchedule, bool, error) {
	var choice int
	var encoded, card, scheduled string
	err := s.db.QueryRowContext(ctx, `SELECT cc.choice,mc.choices,cc.task_id,COALESCE(ms.batch,'') FROM choice_cards cc
		JOIN management_cards mc ON mc.task_id=cc.task_id LEFT JOIN management_schedules ms ON ms.task_id=cc.task_id
		WHERE cc.answer_task_id=?`, task).Scan(&choice, &encoded, &card, &scheduled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, err
	}
	var plans [][]ManagementAction
	if err := json.Unmarshal([]byte(encoded), &plans); err != nil {
		return nil, nil, false, err
	}
	if choice < 0 || choice >= len(plans) {
		return nil, nil, false, errors.New("invalid saved Buddy choice")
	}
	var batch *turnSchedule
	if choice == 0 && scheduled != "" {
		batch = &turnSchedule{Source: card}
		if err := json.Unmarshal([]byte(scheduled), batch); err != nil {
			return nil, nil, false, err
		}
	}
	return plans[choice], batch, true, nil
}

func uniqueManagementName(ctx context.Context, tx *sql.Tx, chat string, t managementTarget) (bool, error) {
	var query string
	switch t.kind {
	case "job":
		query = `SELECT td.title FROM turn_details td JOIN chat_turns ct ON ct.task_id=td.task_id
			JOIN tasks t ON t.id=td.task_id WHERE ct.conversation_id=? AND t.kind='background_work'
			AND t.status IN ('queued','running','needs_input','cancelling')`
	case "routine":
		query = "SELECT name FROM routines WHERE conversation_id=?"
	case "watch":
		query = "SELECT name FROM standing_watches WHERE conversation=? AND enabled>=0"
	default:
		return false, nil
	}
	rows, err := tx.QueryContext(ctx, query, chat)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		if strings.EqualFold(strings.Join(strings.Fields(name), " "), strings.Join(strings.Fields(t.label), " ")) {
			count++
		}
	}
	return count <= 1, rows.Err()
}

func (w *Worker) prepareManagement(ctx context.Context, task string, action *TurnAction) error {
	if !slices.ContainsFunc(action.Manage, func(t ManagementAction) bool { return t.Operation == "resume_watch" }) {
		return nil
	}
	chat, _, err := managementScope(ctx, w.store.db, task)
	if err != nil {
		return err
	}
	action.managementWatchHistory = make(map[string]string)
	action.managementProblems = make(map[string]error)
	for _, t := range action.Manage {
		if t.Operation != "resume_watch" {
			continue
		}
		watch, err := w.store.Watch(ctx, strings.TrimSpace(t.Target))
		if err != nil || watch.ConversationID != chat || watch.Enabled || watch.Source != GmailLabel || watch.ConnectionID == "" {
			continue
		}
		if w.PrepareWatchStart == nil {
			continue
		}
		lookup, cancel := context.WithTimeout(ctx, 12*time.Second)
		err = w.PrepareWatchStart(lookup, &watch)
		cancel()
		if err != nil {
			var invalid *InputError
			if !errors.As(err, &invalid) {
				invalid = &InputError{"I couldn’t reach Gmail to resume this watch. Try again."}
			}
			action.managementProblems[watch.ID] = invalid
		} else {
			action.managementWatchHistory[watch.ID] = watch.GmailHistory
		}
	}
	return ctx.Err()
}

func (w *Worker) signalManagedStops(ids []string) {
	if w.background == nil {
		return
	}
	lane := w.background
	lane.mu.Lock()
	defer lane.mu.Unlock()
	for _, id := range ids {
		if run := lane.runs[id]; run != nil {
			run.cancel()
		}
	}
	lane.Wake()
}
