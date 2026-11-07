package tasks

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"buddy/internal/references"
)

const BackgroundWork = "background_work"
const DevicePool = "any"

const DeviceAttachmentUpgrade = "Update buddyworker on this device to run jobs with attachments."

type Handoff struct {
	DeviceID              string `json:"device_id"`
	AgentID               string `json:"agent_id"`
	Title                 string `json:"title"`
	Brief                 string `json:"brief"`
	UseEarlierAttachments bool   `json:"use_earlier_attachments"`
	ReadOnly              bool   `json:"read_only"`
	After                 []int  `json:"after,omitempty"`
}

type TurnAction struct {
	Reaction                string               `json:"reaction"`
	Learning                []LearnedNote        `json:"learning"`
	Peer                    *PeerQuestion        `json:"ask_peer"`
	BudProposal             *BudDraft            `json:"bud_proposal"`
	Handoff                 *Handoff             `json:"handoff"`
	Handoffs                []Handoff            `json:"handoffs"`
	Routine                 *RoutineDraft        `json:"routine,omitempty"`
	Routines                []RoutineDraft       `json:"routines"`
	Card                    *ChoiceCard          `json:"card"`
	ReadFile                *QuickFileRead       `json:"read_file"`
	SaveFile                *QuickFile           `json:"save_file"`
	Image                   *ImageRequest        `json:"generate_image"`
	GitHubRead              *GitHubQuery         `json:"github_read"`
	GitHubWrite             *GitHubWrite         `json:"github_write"`
	ConnectionRead          *ConnectionQuery     `json:"connection_read"`
	WatchCreate             []WatchDraft         `json:"watch_create"`
	Manage                  []ManagementAction   `json:"manage"`
	ManagementChoices       [][]ManagementAction `json:"management_choices"`
	managementConfirmed     bool
	managementCard          bool
	managementStopped       []string
	managementProblems      map[string]error
	managementWatchHistory  map[string]string
	earlierAttachmentSource string
	fileResult              string
	generatedImage          []byte
	githubResult            *GitHubResult
	githubDraft             *GitHubWrite
	githubWriteCard         bool
	githubWriteRun          bool
	connectionResult        *ConnectionCard
	watches                 []chatWatch
	reactionSaved           bool
	approvedSchedule        *turnSchedule
	scheduledWork           bool
}

func (action *TurnAction) hasHandoff() bool {
	return action.Handoff != nil || len(action.Handoffs) != 0
}

func (action *TurnAction) hasRoutines() bool {
	return action.Routine != nil || len(action.Routines) != 0
}

func (action *TurnAction) hasCard() bool {
	return action != nil && (action.Card != nil || action.BudProposal != nil || action.githubResult != nil ||
		action.connectionResult != nil || len(action.watches) != 0 || action.managementCard || action.githubWriteCard)
}

type RoutineDraft struct {
	DeviceID string `json:"device_id"`
	AgentID  string `json:"agent_id"`
	Name     string `json:"name"`
	Prompt   string `json:"prompt"`
	Minutes  int    `json:"minutes"`
}

type routineSummary struct {
	ID      string    `json:"id"`
	Device  string    `json:"device,omitempty"`
	Name    string    `json:"name"`
	AgentID string    `json:"agent_id"`
	Minutes int       `json:"minutes"`
	NextAt  time.Time `json:"next_at"`
	Enabled bool      `json:"enabled"`
}

type WorkItem struct {
	ChatArchived     bool              `json:"conversation_archived,omitempty"`
	ConversationID   string            `json:"conversation_id"`
	ConversationName string            `json:"conversation_name"`
	CreatedAt        time.Time         `json:"created_at"`
	StartedAt        *time.Time        `json:"started_at,omitempty"`
	FinishedAt       *time.Time        `json:"finished_at,omitempty"`
	Device           string            `json:"device,omitempty"`
	ID               string            `json:"id"`
	AgentID          string            `json:"agent_id"`
	AgentName        string            `json:"agent_name"`
	Title            string            `json:"title"`
	Status           Status            `json:"status"`
	Activity         string            `json:"activity"`
	Kind             ActivityKind      `json:"activity_kind"`
	Update           string            `json:"update,omitempty"`
	CanSteer         bool              `json:"can_steer,omitempty"`
	Steering         []SteeringMessage `json:"steering,omitempty"`
	InputID          string            `json:"input_id,omitempty"`
}

type workRequest struct {
	origin, title, brief, parent, routine, device string
	timeout                                       int
}

func checkQueue(ctx context.Context, db queryer, background bool) error {
	var queued int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM tasks WHERE status='queued' AND (kind='background_work')=?", background).Scan(&queued); err != nil {
		return err
	}
	if queued >= 100 {
		return ErrQueueFull
	}
	return nil
}

func enqueueTurn(ctx context.Context, tx *sql.Tx, kind, key, conversation string, agent Agent, request workRequest) (Task, error) {
	if agent.WorkspaceNeedsReview && request.device == "" {
		return Task{}, &InputError{WorkspaceReviewMessage}
	}
	timeout := 45 * 60
	if request.timeout > 0 {
		timeout = request.timeout
	}
	t := Task{
		ID:      "tsk_" + rand.Text(),
		Request: Request{Kind: kind, Repository: agent.Workspace, TimeoutSeconds: timeout},
		Status:  Queued, Summary: "Waiting to start.", CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO tasks(id,request_key,kind,repo,command,timeout,status,summary,created)
		VALUES (?,?,?,?,'null',?,?,?,?)`, t.ID, namespacedKey(kind, key), t.Kind, t.Repository,
		t.TimeoutSeconds, t.Status, t.Summary, t.CreatedAt.UnixMicro()); err != nil {
		return Task{}, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO chat_turns VALUES (?,?,?,?,?)",
		t.ID, conversation, agent.Name, agent.Instructions, agent.AllowEdits); err != nil {
		return Task{}, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO turn_models VALUES (?,?)", t.ID, agent.Model); err != nil {
		return Task{}, err
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO turn_details VALUES (?,?,?,?,?,?,NULLIF(?,''),NULLIF(?,''))",
		t.ID, agent.ID, agent.Role, request.origin, request.title, request.brief, request.parent, request.routine)
	if err == nil && request.device != "" {
		_, err = tx.ExecContext(ctx, "INSERT INTO device_runs(task_id,device_id) VALUES (?,?)", t.ID, request.device)
	}
	return t, err
}

func saveHandoff(ctx context.Context, tx *sql.Tx, id string, handoff Handoff, earlierSource string) (string, error) {
	return saveHandoffs(ctx, tx, id, []Handoff{handoff}, earlierSource, false)
}

func saveHandoffs(ctx context.Context, tx *sql.Tx, id string, handoffs []Handoff, earlierSource string, allowOutside bool) (string, error) {
	var conversation, requester, repo string
	var allowEdits, readOnlyReview bool
	if err := tx.QueryRowContext(ctx, `SELECT ct.conversation_id,COALESCE(td.agent_id,c.agent_id),t.repo,ct.allow_edits,
		EXISTS(SELECT 1 FROM turn_skills WHERE task_id=ct.task_id AND name=?)
		FROM chat_turns ct JOIN conversations c ON c.id=ct.conversation_id
		JOIN tasks t ON t.id=ct.task_id LEFT JOIN turn_details td ON td.task_id=t.id
		WHERE ct.task_id=?`, reviewSkillName, id).Scan(&conversation, &requester, &repo, &allowEdits, &readOnlyReview); err != nil {
		return "", err
	}
	source, err := requestAttachmentSource(ctx, tx, id)
	if err != nil {
		return "", err
	}
	// The outer transaction can still commit a reply explaining an invalid
	// assignment. Roll back the whole batch before that reply is saved.
	if _, err := tx.ExecContext(ctx, "SAVEPOINT handoff_batch"); err != nil {
		return "", err
	}
	jobs, err := enqueueAssignments(ctx, tx, assignmentSource{
		chat: conversation, requester: requester, key: "handoff:" + id, parent: id,
		repo: repo, allowEdits: allowEdits, readOnly: readOnlyReview, attachment: source, earlier: earlierSource, allowOutside: allowOutside,
	}, handoffs)
	if err != nil {
		if _, rollbackErr := tx.ExecContext(ctx, "ROLLBACK TO handoff_batch"); rollbackErr != nil {
			return "", rollbackErr
		}
	}
	if _, releaseErr := tx.ExecContext(ctx, "RELEASE handoff_batch"); releaseErr != nil {
		return "", releaseErr
	}
	if err != nil {
		return "", err
	}
	if len(jobs) == 1 {
		job := jobs[0]
		link := "[" + escapeTitle(job.Title) + "](" + references.Work(conversation, job.ID) + ")"
		if job.Reused {
			return escapeTitle(job.AgentName) + " is already handling " + link + ". We’ll keep updates in this chat.", nil
		}
		if job.AgentID == requester {
			return "I’ll take care of " + link + job.destination + " and let you know how it goes or if I need anything from you.", nil
		}
		return "I’ve asked " + escapeTitle(job.AgentName) + " to take care of " + link + job.destination + ". We’ll keep you posted here.", nil
	}
	var reply strings.Builder
	reply.WriteString("I’ve handed these out:")
	for _, job := range jobs {
		state := ""
		if job.Reused {
			state = " (already in progress)"
		}
		fmt.Fprintf(&reply, "\n- %s: [%s](%s)%s%s", escapeTitle(job.AgentName), escapeTitle(job.Title), references.Work(conversation, job.ID), job.destination, state)
	}
	reply.WriteString("\n\nWe’ll post results or questions here.")
	return reply.String(), nil
}

func enqueueHandoff(ctx context.Context, tx *sql.Tx, key, chat string, agent Agent, request workRequest) (Task, string, error) {
	request.device = strings.TrimSpace(request.device)
	var destination string
	pool := request.device == DevicePool
	if pool {
		var configured bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM device_pool_members p JOIN devices d ON d.id=p.device_id
			WHERE d.revoked=0 AND (?=0 OR p.allow_edits=1))`, agent.AllowEdits).Scan(&configured); err != nil {
			return Task{}, "", err
		}
		if !configured {
			return Task{}, "", &InputError{"Set up a compatible worker in the shared pool or choose a specific machine."}
		}
		destination, request.device = " through the shared worker pool", ""
		agent.Workspace, agent.WorkspaceNeedsReview = "", false
	} else if request.device != "" {
		name, err := deviceName(ctx, tx, request.device)
		if err != nil {
			return Task{}, "", err
		}
		destination = " on " + escapeTitle(name)
		agent.Workspace = ""
	}
	job, err := enqueueTurn(ctx, tx, BackgroundWork, key, chat, agent, request)
	if err != nil {
		return Task{}, "", err
	}
	if pool {
		if _, err := tx.ExecContext(ctx, "INSERT INTO pooled_work VALUES (?)", job.ID); err != nil {
			return Task{}, "", err
		}
	}
	return job, destination, nil
}

func escapeTitle(value string) string {
	return strings.NewReplacer("\\", "\\\\", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]", "<", "\\<", "`", "\\`").Replace(value)
}

const workSelect = `SELECT t.id,td.agent_id,ct.agent_name,td.title,t.status,
	t.summary,COALESCE((SELECT e.text FROM events e WHERE e.task_id=t.id AND e.stream IN ('commentary','work_update')
		AND (t.started IS NULL OR e.at>=t.started)
		ORDER BY e.seq DESC LIMIT 1),''),` + steeringJSON + `,
	COALESCE(d.name,CASE WHEN p.task_id IS NOT NULL THEN 'Worker pool' ELSE '' END),ct.conversation_id,
	COALESCE(g.name,owner.name),(EXISTS(SELECT 1 FROM removed_buds WHERE agent_id=c.agent_id) OR EXISTS(SELECT 1 FROM archived_groups WHERE conversation_id=c.id)),t.created,t.started,t.finished,
	COALESCE((SELECT q.task_id FROM work_inputs q WHERE q.job_id=t.id AND q.answer_key IS NULL AND t.status='needs_input'),'')
	FROM chat_turns ct JOIN tasks t ON t.id=ct.task_id JOIN turn_details td ON td.task_id=t.id
	JOIN conversations c ON c.id=ct.conversation_id JOIN agents owner ON owner.id=c.agent_id
	LEFT JOIN group_chats g ON g.conversation_id=c.id
	LEFT JOIN device_runs dr ON dr.task_id=t.id LEFT JOIN devices d ON d.id=dr.device_id
	LEFT JOIN pooled_work p ON p.task_id=t.id
	WHERE t.kind='background_work'`

const workOrder = " ORDER BY (t.status IN ('queued','running','needs_input','cancelling')) DESC,t.rowid DESC LIMIT ?"

const WorkOverviewLimit = 150
const ChatWorkOverviewLimit = 60

func listWork(ctx context.Context, db queryer, conversation string) ([]WorkItem, error) {
	return readWork(ctx, db, workSelect+" AND ct.conversation_id=?"+workOrder, conversation, 20)
}

// WorkContext includes the responding Bud's other jobs for status questions.
// Full job details and recent updates stay in the chat where work was assigned.
func (s *Store) WorkContext(ctx context.Context, conversation, agent string) ([]WorkItem, error) {
	local, err := listWork(ctx, s.db, conversation)
	if err != nil {
		return nil, err
	}
	other, err := readWork(ctx, s.db, workSelect+" AND ct.conversation_id<>? AND td.agent_id=?"+workOrder, conversation, agent, 12)
	if err != nil {
		return nil, err
	}
	for i, job := range other {
		other[i] = WorkItem{ID: job.ID, AgentID: job.AgentID, AgentName: job.AgentName, Title: job.Title,
			ConversationID: job.ConversationID, Status: job.Status, CreatedAt: job.CreatedAt, FinishedAt: job.FinishedAt}
	}
	return append(local, other...), nil
}

func (s *Store) WorkOverview(ctx context.Context, conversation string) ([]WorkItem, error) {
	if conversation != "" {
		return readWork(ctx, s.db, workSelect+" AND ct.conversation_id=?"+workOrder, conversation, ChatWorkOverviewLimit)
	}
	return readWork(ctx, s.db, workSelect+workOrder, WorkOverviewLimit)
}

type WorkHistoryPage struct {
	Items []WorkItem `json:"items"`
	Next  string     `json:"next,omitempty"`
}

func (s *Store) WorkHistory(ctx context.Context, conversation, before string) (WorkHistoryPage, error) {
	const size = 50
	query := workSelect + " AND t.status NOT IN ('queued','running','needs_input','cancelling')"
	var args []any
	if conversation != "" {
		query += " AND ct.conversation_id=?"
		args = append(args, conversation)
	}
	if before != "" {
		query += " AND t.rowid<(SELECT previous.rowid FROM tasks previous WHERE previous.id=?)"
		args = append(args, before)
	}
	items, err := readWork(ctx, s.db, query+" ORDER BY t.rowid DESC LIMIT ?", append(args, size+1)...)
	page := WorkHistoryPage{Items: items}
	if len(items) > size {
		page.Items, page.Next = items[:size], items[size-1].ID
	}
	return page, err
}

func readWork(ctx context.Context, db queryer, query string, args ...any) ([]WorkItem, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var work []WorkItem
	for rows.Next() {
		var item WorkItem
		var corrections string
		var created int64
		var started, finished sql.NullInt64
		if err := rows.Scan(&item.ID, &item.AgentID, &item.AgentName, &item.Title, &item.Status,
			&item.Activity, &item.Update, &corrections, &item.Device, &item.ConversationID,
			&item.ConversationName, &item.ChatArchived, &created, &started, &finished, &item.InputID); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(corrections), &item.Steering); err != nil {
			return nil, err
		}
		item.CreatedAt = time.UnixMicro(created).UTC()
		if started.Valid {
			t := time.UnixMicro(started.Int64).UTC()
			item.StartedAt = &t
		}
		if finished.Valid {
			t := time.UnixMicro(finished.Int64).UTC()
			item.FinishedAt = &t
		}
		item.Kind = activityKind(item.Activity)
		if item.Status != Running {
			item.Update = ""
		}
		work = append(work, item)
	}
	return work, rows.Err()
}

const MinRoutineMinutes, MaxRoutineMinutes = 1, 7 * 24 * 60

type Routine struct {
	DeviceID           string    `json:"device_id,omitempty"`
	DeviceDisconnected bool      `json:"device_disconnected,omitempty"`
	Device             string    `json:"device,omitempty"`
	ID                 string    `json:"id"`
	AgentID            string    `json:"agent_id"`
	Name               string    `json:"name"`
	Prompt             string    `json:"prompt"`
	Minutes            int       `json:"minutes"`
	NextAt             time.Time `json:"next_at"`
	Enabled            bool      `json:"enabled"`
}

func RoutineCadence(minutes int) string {
	switch {
	case minutes == 1:
		return "Every minute"
	case minutes == 60:
		return "Hourly"
	case minutes == 1440:
		return "Daily"
	case minutes == 10080:
		return "Weekly"
	case minutes%1440 == 0:
		return fmt.Sprintf("Every %d days", minutes/1440)
	case minutes%60 == 0:
		return fmt.Sprintf("Every %d hours", minutes/60)
	default:
		return fmt.Sprintf("Every %d minutes", minutes)
	}
}

func listRoutines(ctx context.Context, db queryer, conversation string) ([]Routine, error) {
	rows, err := db.QueryContext(ctx, `SELECT r.id,r.agent_id,r.name,r.prompt,r.minutes,r.next_at,r.enabled,COALESCE(rd.device_id,''),COALESCE(d.name,''),COALESCE(d.revoked,0)
		FROM routines r LEFT JOIN routine_devices rd ON rd.routine_id=r.id LEFT JOIN devices d ON d.id=rd.device_id
		WHERE r.conversation_id=? ORDER BY r.rowid DESC`, conversation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []Routine
	for rows.Next() {
		var routine Routine
		var at int64
		if err := rows.Scan(&routine.ID, &routine.AgentID, &routine.Name, &routine.Prompt, &routine.Minutes, &at, &routine.Enabled, &routine.DeviceID, &routine.Device, &routine.DeviceDisconnected); err != nil {
			return nil, err
		}
		routine.NextAt = time.UnixMicro(at).UTC()
		items = append(items, routine)
	}
	return items, rows.Err()
}

func (s *Store) AddRoutine(ctx context.Context, conversation string, routine Routine) (Routine, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Routine{}, err
	}
	defer tx.Rollback()
	routine, err = addRoutine(ctx, tx, conversation, routine)
	if err != nil {
		return Routine{}, err
	}
	return routine, tx.Commit()
}

func addRoutine(ctx context.Context, tx *sql.Tx, conversation string, routine Routine) (Routine, error) {
	routine.Name, routine.Prompt = strings.TrimSpace(routine.Name), strings.TrimSpace(routine.Prompt)
	if err := validateRoutine(routine.Name, routine.Prompt, routine.Minutes); err != nil {
		return Routine{}, err
	}
	if err := requireOpenConversation(ctx, tx, conversation); err != nil {
		return Routine{}, err
	}
	agent, err := conversationParticipant(ctx, tx, conversation, routine.AgentID)
	if err != nil {
		return Routine{}, err
	}
	if routine.DeviceID != "" {
		routine.Device, err = deviceName(ctx, tx, routine.DeviceID)
		if err != nil {
			return Routine{}, err
		}
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM routines WHERE conversation_id=?", conversation).Scan(&count); err != nil {
		return Routine{}, err
	}
	if count >= 25 {
		return Routine{}, &InputError{"This chat can have up to 25 routines."}
	}
	routine.ID, routine.AgentID, routine.Enabled = "rtn_"+rand.Text(), agent.ID, true
	routine.NextAt = time.Now().UTC().Truncate(time.Microsecond).Add(time.Duration(routine.Minutes) * time.Minute)
	if _, err := tx.ExecContext(ctx, "INSERT INTO routines VALUES (?,?,?,?,?,?,?,1)", routine.ID, conversation,
		routine.AgentID, routine.Name, routine.Prompt, routine.Minutes, routine.NextAt.UnixMicro()); err != nil {
		return Routine{}, err
	}
	if routine.DeviceID != "" {
		if _, err := tx.ExecContext(ctx, "INSERT INTO routine_devices VALUES (?,?)", routine.ID, routine.DeviceID); err != nil {
			return Routine{}, err
		}
	}
	return routine, nil
}

func validateRoutine(name, prompt string, minutes int) error {
	if !validTitle(name, 120) || prompt == "" || len(prompt) > 12000 || minutes < MinRoutineMinutes || minutes > MaxRoutineMinutes {
		return &InputError{"Enter a name, instructions, and an interval between 1 minute and 7 days."}
	}
	return nil
}

func saveRequestedRoutine(ctx context.Context, tx *sql.Tx, id string, draft RoutineDraft) (string, error) {
	var conversation, requester string
	if err := tx.QueryRowContext(ctx, `SELECT ct.conversation_id,COALESCE(td.agent_id,c.agent_id)
		FROM chat_turns ct JOIN conversations c ON c.id=ct.conversation_id
		LEFT JOIN turn_details td ON td.task_id=ct.task_id WHERE ct.task_id=?`, id).
		Scan(&conversation, &requester); err != nil {
		return "", err
	}
	agent := strings.TrimSpace(draft.AgentID)
	if agent == "" {
		agent = requester
	}
	routine, err := addRoutine(ctx, tx, conversation, Routine{
		AgentID: agent, Name: draft.Name, Prompt: draft.Prompt, Minutes: draft.Minutes, DeviceID: strings.TrimSpace(draft.DeviceID),
	})
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO routine_skill_sources(routine_id,task_id)
		SELECT ?,task_id FROM turn_skills WHERE task_id=?`, routine.ID, id); err != nil {
		return "", err
	}
	confirmation := "Routine scheduled · **" + escapeTitle(routine.Name) + "** · " + RoutineCadence(routine.Minutes)
	if routine.Device != "" {
		confirmation += " · " + escapeTitle(routine.Device)
	}
	return confirmation, nil
}

func saveTurnAction(ctx context.Context, tx *sql.Tx, id string, action *TurnAction, reply string) (string, error) {
	action.reactionSaved = false
	var archived bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM chat_turns ct JOIN archived_groups a
		ON a.conversation_id=ct.conversation_id WHERE ct.task_id=?)`, id).Scan(&archived); err != nil {
		return "", err
	}
	if archived && !action.githubWriteRun {
		*action = TurnAction{}
		return "This group was archived during the reply.", nil
	}
	if action.githubDraft != nil {
		saved, err := saveGitHubWrite(ctx, tx, id, *action.githubDraft)
		action.githubWriteCard = saved
		if err == nil && !saved {
			return "That GitHub connection no longer has collaboration enabled. Check it in Connections.", nil
		}
		return "", err
	}
	if action.Reaction != "" && !action.hasCard() && !action.hasManagement() && !action.hasHandoff() &&
		action.Peer == nil && !action.hasRoutines() && action.ReadFile == nil && action.SaveFile == nil &&
		action.Image == nil && len(action.generatedImage) == 0 && action.GitHubRead == nil &&
		action.ConnectionRead == nil && len(action.WatchCreate) == 0 {
		saved, err := saveBudReaction(ctx, tx, id, action.Reaction)
		action.reactionSaved = saved
		if err == nil && !saved && reply == "" {
			return "I couldn’t add that reaction.", nil
		}
		return "", err
	}
	if len(action.generatedImage) != 0 {
		_, err := tx.ExecContext(ctx, "INSERT INTO generated_images VALUES (?,?)", id, action.generatedImage)
		return "Here’s the image.", err
	}
	if action.Peer != nil {
		if action.hasManagement() || action.hasHandoff() || action.hasRoutines() || action.Card != nil ||
			action.ReadFile != nil || action.SaveFile != nil || action.BudProposal != nil || action.GitHubRead != nil ||
			action.ConnectionRead != nil || len(action.WatchCreate) != 0 {
			*action = TurnAction{}
			return "I can bring in another Bud or make that change separately.", nil
		}
		return enqueuePeerReply(ctx, tx, id, *action.Peer)
	}
	if action.hasManagement() {
		return saveManagementTurn(ctx, tx, id, action)
	}
	if len(action.watches) != 0 {
		return "", saveChatWatchDraft(ctx, tx, id, action.watches)
	}
	if action.connectionResult != nil {
		return "", saveConnectionCard(ctx, tx, id, *action.connectionResult)
	}
	if action.githubResult != nil {
		result, err := json.Marshal(action.githubResult)
		if err != nil {
			return "", err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO github_chat_results(task_id,result) VALUES (?,?)", id, string(result)); err != nil {
			return "", err
		}
	}
	switch {
	case action.BudProposal != nil:
		if action.Card != nil || action.hasHandoff() || action.hasRoutines() || action.ReadFile != nil || action.SaveFile != nil {
			return "I can draft a Bud or start another action in this reply. Which would you like first?", nil
		}
		err := saveBudProposal(ctx, tx, id, *action.BudProposal)
		var invalid *InputError
		if errors.As(err, &invalid) {
			return "I couldn’t show the Bud draft. " + invalid.Message, nil
		}
		return "", err
	case action.SaveFile != nil && (action.hasHandoff() || action.hasRoutines()) && action.Card == nil:
		return "I can save a small file here or hand off the work. Which would you like?", nil
	case action.Card != nil && action.hasRoutines() && !action.hasHandoff():
		return "I need to resolve your choice before starting any work.", nil
	case action.Card != nil:
		err := saveChoiceCard(ctx, tx, id, *action.Card)
		var invalid *InputError
		if errors.As(err, &invalid) {
			if question := strings.TrimSpace(action.Card.Question); action.hasHandoff() && question != "" && len(question) <= 500 {
				return question, nil
			}
			return "I couldn't show the choices. " + invalid.Message, nil
		}
		return "", err
	case action.hasRoutines():
		return saveScheduledTurn(ctx, tx, id, action, reply)
	case action.hasHandoff():
		handoffs := action.Handoffs
		if action.Handoff != nil {
			if len(handoffs) != 0 {
				return "I couldn’t start the assignments. Please send the request again.", nil
			}
			handoffs = []Handoff{*action.Handoff}
		}
		var note string
		var err error
		if action.Handoff != nil {
			note, err = saveHandoff(ctx, tx, id, *action.Handoff, action.earlierAttachmentSource)
		} else {
			note, err = saveHandoffs(ctx, tx, id, handoffs, action.earlierAttachmentSource, true)
		}
		var invalid *InputError
		switch {
		case errors.Is(err, ErrQueueFull):
			return "I couldn’t start that yet. I’m at capacity for background work right now.", nil
		case errors.As(err, &invalid):
			return "I couldn’t start that. " + invalid.Message, nil
		case err == nil && action.Handoff == nil && strings.TrimSpace(reply) != "":
			return strings.TrimSpace(reply) + "\n\n" + note, nil
		case err == nil && strings.TrimSpace(reply) != "":
			return reply, nil
		default:
			return note, err
		}
	case action.SaveFile != nil:
		if action.fileResult == "" {
			return "I couldn’t save the file in my working folder.", nil
		}
		return action.fileResult, nil
	default:
		return "", nil
	}
}

func (s *Store) UpdateRoutine(ctx context.Context, conversation, id string, enabled *bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := updateRoutine(ctx, tx, conversation, id, enabled); err != nil {
		return err
	}
	return tx.Commit()
}

func updateRoutine(ctx context.Context, db writeQueryer, conversation, id string, enabled *bool) error {
	if err := requireOpenConversation(ctx, db, conversation); err != nil {
		return err
	}
	var r sql.Result
	var err error
	if enabled == nil {
		r, err = db.ExecContext(ctx, "DELETE FROM routines WHERE conversation_id=? AND id=?", conversation, id)
	} else {
		r, err = db.ExecContext(ctx, `UPDATE routines SET enabled=?,
			next_at=CASE WHEN enabled=0 AND ? THEN ?+minutes*60000000 ELSE next_at END
			WHERE conversation_id=? AND id=? AND (NOT ? OR NOT EXISTS(
				SELECT 1 FROM routine_devices rd JOIN devices d ON d.id=rd.device_id WHERE rd.routine_id=routines.id AND d.revoked=1))
			AND (NOT ? OR NOT EXISTS(SELECT 1 FROM removed_buds WHERE agent_id=routines.agent_id))`,
			*enabled, *enabled, time.Now().UnixMicro(), conversation, id, *enabled, *enabled)
	}
	if err != nil {
		return err
	}
	if n, err := r.RowsAffected(); err != nil {
		return err
	} else if n != 0 {
		return nil
	}
	if enabled != nil && *enabled {
		var disconnected, removed bool
		err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM routines r JOIN routine_devices rd ON rd.routine_id=r.id
			JOIN devices d ON d.id=rd.device_id WHERE r.conversation_id=? AND r.id=? AND d.revoked=1),
			EXISTS(SELECT 1 FROM routines r JOIN removed_buds b ON b.agent_id=r.agent_id WHERE r.conversation_id=? AND r.id=?)`,
			conversation, id, conversation, id).Scan(&disconnected, &removed)
		if err != nil {
			return err
		}
		if removed {
			return &InputError{"This routine belongs to a removed Bud."}
		}
		if disconnected {
			return &InputError{"This routine's device was disconnected. Create a new routine on an available device."}
		}
	}
	return ErrNotFound
}

// Advance the schedule and enqueue its work in one transaction. If the server
// was offline, enqueue one overdue occurrence and advance to the next one.
func (s *Store) enqueueDueRoutine(ctx context.Context, now time.Time) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var routine Routine
	var conversation, skillSource string
	var next int64
	err = tx.QueryRowContext(ctx, `SELECT r.id,r.conversation_id,r.agent_id,r.name,r.prompt,r.minutes,r.next_at,
		COALESCE(rd.device_id,''),COALESCE(rs.task_id,'') FROM routines r
		LEFT JOIN routine_devices rd ON rd.routine_id=r.id LEFT JOIN routine_skill_sources rs ON rs.routine_id=r.id
		WHERE r.enabled=1 AND r.next_at<=? AND NOT EXISTS(
			SELECT 1 FROM turn_details td JOIN tasks t ON t.id=td.task_id WHERE td.routine_id=r.id
				AND t.status IN ('queued','running','needs_input','cancelling'))
			AND (rd.device_id IS NOT NULL OR NOT EXISTS(
				SELECT 1 FROM restored_workspace_agents h WHERE h.agent_id=r.agent_id))
		ORDER BY r.next_at,r.id LIMIT 1`, now.UnixMicro()).
		Scan(&routine.ID, &conversation, &routine.AgentID, &routine.Name, &routine.Prompt, &routine.Minutes, &next, &routine.DeviceID, &skillSource)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := checkQueue(ctx, tx, true); err != nil {
		return false, err
	}
	agent, err := conversationParticipant(ctx, tx, conversation, routine.AgentID)
	if err != nil {
		return false, err
	}
	if routine.DeviceID != "" {
		agent.Workspace = ""
	}
	job, err := enqueueTurn(ctx, tx, BackgroundWork, routine.ID+":"+time.UnixMicro(next).UTC().Format(time.RFC3339Nano),
		conversation, agent, workRequest{origin: "routine", title: routine.Name, brief: routine.Prompt, routine: routine.ID, device: routine.DeviceID})
	if err != nil {
		return false, err
	}
	if skillSource != "" {
		if err := continueSkill(ctx, tx, job.ID, skillSource); err != nil {
			return false, err
		}
	}
	interval := (time.Duration(routine.Minutes) * time.Minute).Microseconds()
	next += ((now.UnixMicro()-next)/interval + 1) * interval
	if _, err := tx.ExecContext(ctx, "UPDATE routines SET next_at=? WHERE id=?", next, routine.ID); err != nil {
		return false, err
	}
	return true, tx.Commit()
}
