package tasks

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

type BudDraft struct {
	Name         string `json:"name"`
	Role         string `json:"role"`
	Instructions string `json:"instructions"`
	CreateNow    bool   `json:"create_now,omitempty"`
	Evidence     string `json:"evidence,omitempty"`
}

type BudProposal struct {
	BudDraft
	ConversationID string   `json:"conversation_id,omitempty"`
	NeedsRevision  *bool    `json:"needs_revision,omitempty"`
	PendingWork    []string `json:"pending_work,omitempty"`
}

var (
	budCreationCommand = regexp.MustCompile(`(?i)^(?:(?:please|snälla),?\s+)?(?:(?:can you|could you|would you|kan du)\s+)?(?:(?:please|snälla),?\s+)?(?:(?:create|make) (?:a|one) (?:new )?bud|skapa en (?:ny )?bud)(?:$|\s)`)
	budDraftRequest    = regexp.MustCompile(`(?i)^(?:(?:please|snälla),?\s+)?(?:(?:can you|could you|would you|kan du)\s+)?(?:(?:please|snälla),?\s+)?(?:(?:draft|design|propose) (?:a|one) (?:new )?bud|skissa (?:en|på en) (?:ny )?bud)(?:$|[^\pL])`)
	budCreationCaveat  = regexp.MustCompile(`(?i)(?:^|[^\pL])(?:if|unless|hypothetical(?:ly)?|pretend|imagine|example|simulate|don't|do not|instead of|never|as a draft|as a preview|only (?:after|once|when)|cancel (?:that|it)|hold off|actually wait|inte|om|låtsas|exempel|utkast)(?:$|[^\pL])`)
	budCreationDelay   = regexp.MustCompile(`(?i)(?:^|[^\pL])(?:after|before|once|when|until|upon|pending|wait|later|tomorrow|as soon as|as long as|the moment|next (?:week|month|year)|(?:on|next) (?:monday|tuesday|wednesday|thursday|friday|saturday|sunday)|vänta|senare|imorgon|i morgon|så snart|innan|efter|när|tills)(?:$|[^\pL])`)
	budCreationReview  = regexp.MustCompile(`(?i)(?:^|[^\pL])(?:but|however|except|provided|subject to|contingent (?:on|upon)|assuming|first (?:show|ask|confirm|let)|(?:following|on|with|awaiting|without)\s+(?:(?:the|my|our|your|final|written)\s+)*(?:approval|confirmation|go[- ]ahead|sign[- ]?off|authorization)|(?:my|our) (?:approval|confirmation|go[- ]ahead|sign[- ]?off)|(?:ask|let|have) (?:me|us)(?:\s+to)?\s+(?:approve|confirm|review|sign[- ]off|authorize)|(?:ask|check) (?:me|us) (?:first|beforehand)|men|förutom|förutsatt att|mitt godkännande)(?:$|[^\pL])`)
	budCreationPreview = regexp.MustCompile(`(?i)(?:^|[^\pL])(?:(?:(?:show|send|share|give) me|(?:let me|I (?:want|need) to) (?:see|read|review)) (?:an? |the |your |its |his |her |their )?(?:(?:full |initial |proposed )?(?:draft|preview|proposal)|(?:it|profile|instructions) first)|(?:keep|leave|save) (?:it (?:as|in) (?:an? |the )?|(?:the |its |your )?)(?:draft|preview|proposal)|draft (?:only|mode)|for (?:my )?review(?: first)?[.!?]*$|visa mig (?:ett |det |dess )?(?:utkast|förslag)|för min granskning)(?:$|[^\pL])`)
	budRecurringTiming = regexp.MustCompile(`(?i)\b(?:(?:after|before)\s+(?:each|every)|(?:efter|innan)\s+varje)\s+`)
	budCreationPolite  = regexp.MustCompile(`(?i),?\s+(?:please|tack)$`)
	budNamePrefix      = regexp.MustCompile(`(?i)^(?:named|called|som heter)\s+`)
	budPurposePrefix   = regexp.MustCompile(`(?i)^(?:for|to|with|as|that|who|whose|för|med|som)\s+\S`)
	budPurposeJoin     = regexp.MustCompile(`(?i)(?:^|[^\pL])(?:and|or|och|eller)(?:$|[^\pL])`)
	budSentenceBreak   = regexp.MustCompile(`[.!?]\s+\S`)
	budCreationThen    = regexp.MustCompile(`(?i)(?:[.!?,]\s+|,?\s+(?:and|och)\s+)(?:(?:then|sedan)\s+)?(?:assign|ask|have|give|start|run|queue|schedule|tilldela|be|starta|schemalägg)\s+`)
	budFollowupUser    = regexp.MustCompile(`(?i)(?:^|[^\pL])(?:ask|let|have) (?:me|us)(?:$|[^\pL])|(?:^|[^\pL])be (?:mig|oss)(?:$|[^\pL])`)
	budCompletionNote  = regexp.MustCompile(`(?i)(?:^|[^\pL])(?:let (?:me|us) know|meddela (?:mig|oss))(?: (?:when|once|när) (?:it(?:'s| is)|you(?:'re| are)|the (?:job|work) is|det är|du är) (?:done|finished|complete|klart|klar))?(?:$|[.!?])`)
	budAddressGreeting = regexp.MustCompile(`(?i)^(?:hey|hi|hello|hej)\s+`)
)

// BudCreationFollowup reports whether a verified creation command also asks
// for a separate action in the same turn.
func BudCreationFollowup(request string) bool { return budCreationThen.MatchString(request) }

func authorizesBudCreation(request, evidence, speaker, name string) bool {
	request, evidence = strings.TrimSpace(request), strings.TrimSpace(evidence)
	if !userTurnEvidence(request, evidence) || budCreationHasQuote(request) {
		return false
	}
	// Speech and typed requests can use different spaces between the same words.
	words := strings.ReplaceAll(strings.Join(strings.Fields(request), " "), "’", "'")
	withoutReport := budCompletionNote.ReplaceAllString(words, " ")
	if budCreationCaveat.MatchString(words) || budCreationReview.MatchString(words) || budCreationPreview.MatchString(words) || budFollowupUser.MatchString(withoutReport) ||
		budCreationDelay.MatchString(budRecurringTiming.ReplaceAllString(withoutReport, " ")) {
		return false
	}
	// Require evidence to include the opening command, with or without its address.
	if prefix := budAddressLength(request, speaker); prefix != 0 {
		if strings.HasPrefix(evidence, request[:prefix]) {
			evidence = strings.TrimSpace(evidence[prefix:])
		}
		request = strings.TrimSpace(request[prefix:])
	}
	if !strings.HasPrefix(request, evidence) || !budCreationCommand.MatchString(strings.TrimRight(evidence, " .!?")) {
		return false
	}
	if next := budCreationThen.FindStringIndex(request); next != nil {
		request = request[:next[0]]
	}
	request = budCreationPolite.ReplaceAllString(strings.TrimRight(request, " .!?"), "")
	if budSentenceBreak.MatchString(request) {
		return false
	}
	prefix := budCreationCommand.FindStringIndex(request)
	if prefix == nil {
		return false
	}
	details := strings.TrimSpace(request[prefix[1]:])
	if named := budNamePrefix.FindStringIndex(details); named != nil {
		details = details[named[1]:]
		// A requested name must match the saved name, including multi-word names.
		if strings.EqualFold(details, name) {
			return true
		}
		if len(details) <= len(name) || details[len(name)] != ' ' || !strings.EqualFold(details[:len(name)], name) {
			return false
		}
		details = strings.TrimSpace(details[len(name):])
	}
	// Additional clauses may qualify the request. Keep them in the draft
	// unless they were already recognized as a separate follow-up above.
	return details == "" || !strings.ContainsAny(details, ",;:—–") &&
		budPurposePrefix.MatchString(details) && !budPurposeJoin.MatchString(details)
}

func budCreationHasQuote(text string) bool {
	if strings.ContainsAny(text, "\r\n\"“”‘`»«") {
		return true
	}
	for i, ch := range text {
		if ch != '\'' && ch != '’' {
			continue
		}
		before, _ := utf8.DecodeLastRuneInString(text[:i])
		after, _ := utf8.DecodeRuneInString(text[i+utf8.RuneLen(ch):])
		if !unicode.IsLetter(before) || !unicode.IsLetter(after) {
			return true
		}
	}
	return false
}

func budAddressLength(request, speaker string) int {
	i := strings.IndexAny(request, ",:")
	if i < 0 || speaker == "" {
		return 0
	}
	address := budAddressGreeting.ReplaceAllString(strings.TrimSpace(request[:i]), "")
	if strings.EqualFold(strings.TrimPrefix(address, "@"), speaker) {
		return i + 1
	}
	return 0
}

type budProvisionError struct{ cause error }

func (e *budProvisionError) Error() string { return e.cause.Error() }
func (e *budProvisionError) Unwrap() error { return e.cause }

const budProvisionNotice = "I saved the Bud as a draft because its workspace or profile folder is unavailable. You can retry from the card."

// Workspace and profile provisioning happen before any agent rows are written.
// A host filesystem error leaves the saved proposal available to retry.
func (s *Store) provisionProposedBud(ctx context.Context, tx *sql.Tx, profile Agent) (Agent, string, error) {
	bud, err := s.saveAgentInTx(ctx, tx, profile.ID, profile, true, false)
	var provision *budProvisionError
	if errors.As(err, &provision) {
		log.Printf("provision Bud %s: %v", profile.ID, provision)
		return Agent{}, budProvisionNotice, nil
	}
	return bud, "", err
}

func validateBudDraft(draft *BudDraft) error {
	draft.Name, draft.Role = strings.TrimSpace(draft.Name), strings.TrimSpace(draft.Role)
	draft.Instructions = strings.TrimSpace(draft.Instructions)
	if !validTitle(draft.Name, 100) || !validTitle(draft.Role, 100) || draft.Instructions == "" || len(draft.Instructions) > 8000 || strings.ContainsRune(draft.Instructions, 0) {
		return &InputError{"A Bud needs a short name and role, and instructions under 8,000 bytes."}
	}
	return nil
}

func saveBudProposal(ctx context.Context, tx *sql.Tx, id string, draft BudDraft) (Agent, error) {
	if err := validateBudDraft(&draft); err != nil {
		return Agent{}, err
	}
	profile := Agent{ID: "agt_" + rand.Text(), Name: draft.Name, Role: draft.Role, Instructions: draft.Instructions}
	_, err := tx.ExecContext(ctx, "INSERT INTO bud_proposals VALUES (?,?,?,?,?)",
		id, profile.ID, profile.Name, profile.Role, profile.Instructions)
	return profile, err
}

func saveBudRevision(ctx context.Context, tx *sql.Tx, id string, action *TurnAction) (bool, error) {
	if action == nil || action.BudProposal == nil && action.Card == nil {
		return false, nil
	}
	if action.BudProposal != nil && validateBudDraft(action.BudProposal) != nil {
		return false, nil
	}
	_, pending, err := partitionBudWork(action)
	if err != nil {
		return false, nil
	}
	if action.Card != nil {
		if err := saveBudChoice(ctx, tx, id, *action.Card); err != nil {
			var invalid *InputError
			if errors.As(err, &invalid) {
				return false, nil
			}
			return false, err
		}
	}
	if action.BudProposal != nil {
		if _, err := saveBudProposal(ctx, tx, id, *action.BudProposal); err != nil {
			return false, err
		}
	}
	if len(pending) != 0 {
		err = saveBudProposalWork(ctx, tx, id, pending, action.earlierAttachmentSource)
	} else {
		_, err = tx.ExecContext(ctx, questionRequestChain+`
			INSERT INTO bud_proposal_work SELECT ?,p.handoffs,p.earlier_attachment_source
			FROM requests r JOIN bud_proposal_work p ON p.task_id=r.id
			JOIN tasks t ON t.id=r.id ORDER BY t.rowid DESC LIMIT 1`, id, id)
	}
	return err == nil, err
}

func saveBudProposalWork(ctx context.Context, tx *sql.Tx, proposal string, handoffs []Handoff, earlier string) error {
	if len(handoffs) == 0 {
		return nil
	}
	data, err := json.Marshal(handoffs)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO bud_proposal_work VALUES (?,?,?)", proposal, string(data), earlier); err != nil {
		return err
	}
	// Automatic placement uses the first project's bindings. A host first
	// named in a later answer gets its own snapshot, including a missing path.
	_, err = tx.ExecContext(ctx, questionRequestChain+`, assignments(task_id,project_id,host_id) AS (
			SELECT r.id,json_extract(j.value,'$.project_id'),trim(json_extract(j.value,'$.device_id'))
			FROM requests r JOIN bud_proposal_work w ON w.task_id=r.id JOIN json_each(w.handoffs) j
		)
		INSERT INTO bud_project_checkouts
		SELECT a.task_id,a.project_id,b.host_id,b.path FROM assignments a
		JOIN projects p ON p.id=a.project_id JOIN project_bindings b ON b.project_id=p.id
		WHERE a.task_id=? AND p.archived=0 AND NOT EXISTS (
			SELECT 1 FROM assignments prior WHERE prior.task_id<>a.task_id AND prior.project_id=a.project_id)
		UNION
		SELECT a.task_id,a.project_id,a.host_id,COALESCE(b.path,'') FROM assignments a
		JOIN projects p ON p.id=a.project_id
		LEFT JOIN project_bindings b ON b.project_id=p.id AND b.host_id=a.host_id
		WHERE a.task_id=? AND p.archived=0 AND a.host_id NOT IN ('',?) AND NOT EXISTS (
			SELECT 1 FROM assignments prior WHERE prior.task_id<>a.task_id
			AND prior.project_id=a.project_id AND prior.host_id=a.host_id)`, proposal, proposal, proposal, DevicePool)
	return err
}

func savedProjectCheckout(ctx context.Context, tx *sql.Tx, draft, project, host string, namedHost bool) (string, error) {
	var path string
	err := tx.QueryRowContext(ctx, budProjectSource+`
		SELECT b.path FROM requests r JOIN tasks t ON t.id=r.id JOIN bud_project_checkouts b ON b.task_id=r.id
		WHERE b.project_id=? AND b.host_id=? AND (b.task_id=(SELECT id FROM source)
			OR (? AND t.rowid>(SELECT row_number FROM first_assignment)))
		ORDER BY (t.rowid>(SELECT row_number FROM first_assignment)) DESC,t.rowid LIMIT 1`, draft, project, project, project, host, namedHost).Scan(&path)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return path, err
}

// Automatic placement can give a job a device ID. Read the original handoff
// to distinguish it from a worker explicitly selected in the draft.
func draftJobNamesWorker(ctx context.Context, tx *sql.Tx, job, draft string) (bool, error) {
	var data, key string
	if err := tx.QueryRowContext(ctx, `SELECT w.handoffs,t.request_key FROM bud_proposal_work w
		JOIN tasks t ON t.id=? WHERE w.task_id=?`, job, draft).Scan(&data, &key); err != nil {
		return false, err
	}
	var handoffs []Handoff
	if err := json.Unmarshal([]byte(data), &handoffs); err != nil {
		return false, err
	}
	for i, handoff := range handoffs {
		if key == namespacedKey(BackgroundWork, assignmentKey("bud-approval:"+draft, i)) {
			device := strings.TrimSpace(handoff.DeviceID)
			return device != "" && device != DevicePool, nil
		}
	}
	// Earlier servers could attach draft restrictions to an existing job.
	// That job retains the named checkout policy it was given at the time.
	return true, nil
}

// Earlier builds could record a checkout on the initial question. Use that
// snapshot when there is none on the turn that first assigned the project.
const budProjectSource = questionRequestChain + `, first_assignment(row_number) AS (
	SELECT t.rowid FROM requests r JOIN tasks t ON t.id=r.id
	JOIN bud_proposal_work w ON w.task_id=r.id JOIN json_each(w.handoffs) j
	WHERE json_extract(j.value,'$.project_id')=? ORDER BY t.rowid LIMIT 1
), source(id) AS (
	SELECT r.id FROM requests r JOIN tasks t ON t.id=r.id
	WHERE t.rowid<=(SELECT row_number FROM first_assignment)
	AND EXISTS (SELECT 1 FROM bud_project_checkouts b WHERE b.task_id=r.id AND b.project_id=?)
	ORDER BY (t.rowid=(SELECT row_number FROM first_assignment)) DESC,t.rowid LIMIT 1
)`

func savedPoolCheckouts(ctx context.Context, tx *sql.Tx, draft, project string) (map[string]string, error) {
	rows, err := tx.QueryContext(ctx, budProjectSource+`
		SELECT b.host_id,b.path FROM bud_project_checkouts b JOIN devices d ON d.id=b.host_id
		WHERE b.task_id=(SELECT id FROM source) AND b.project_id=? AND b.path<>'' AND d.revoked=0`, draft, project, project, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	paths := make(map[string]string)
	for rows.Next() {
		var host, path string
		if err := rows.Scan(&host, &path); err != nil {
			return nil, err
		}
		paths[host] = path
	}
	return paths, rows.Err()
}

func migrateBudProposalWork(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var legacy bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pragma_foreign_key_list('bud_proposal_work')
		WHERE "table"='bud_proposals')`).Scan(&legacy); err != nil || !legacy {
		return err
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE bud_proposal_work RENAME TO legacy_bud_proposal_work;
		CREATE TABLE bud_proposal_work (
			task_id TEXT PRIMARY KEY REFERENCES chat_turns(task_id),
			handoffs TEXT NOT NULL,
			earlier_attachment_source TEXT NOT NULL
		);
		INSERT INTO bud_proposal_work SELECT * FROM legacy_bud_proposal_work;
		DROP TABLE legacy_bud_proposal_work;`); err != nil {
		return err
	}
	return tx.Commit()
}

func isBudRevision(ctx context.Context, db queryer, answer string) (bool, error) {
	var revision bool
	err := db.QueryRowContext(ctx, `SELECT 1 FROM choice_card_answers a
		JOIN bud_choice_cards b ON b.task_id=a.question_task_id
		LEFT JOIN turn_details td ON td.task_id=a.question_task_id
		WHERE a.answer_task_id=?
		AND COALESCE(td.origin,'chat')<>?
		AND NOT EXISTS(SELECT 1 FROM github_write_cards g WHERE g.task_id=a.question_task_id)
		AND NOT EXISTS(SELECT 1 FROM management_cards p WHERE p.task_id=a.question_task_id)`, answer, backgroundRequest).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return revision, err
}

// BudDraftRequest reports whether a caller opened with a request for a Bud.
func BudDraftRequest(text, speaker string) bool {
	text = strings.TrimSpace(text)
	text = strings.TrimSpace(text[budAddressLength(text, speaker):])
	return budCreationCommand.MatchString(strings.TrimRight(text, " .!?")) || budDraftRequest.MatchString(text)
}

func migrateBudChoiceCards(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil || version >= 3 {
		return err
	}
	// Older cards did not always record their purpose. Recover recognizable
	// Bud questions without reclassifying planner choices or job receipts.
	rows, err := tx.QueryContext(ctx, `SELECT q.task_id,m.text,ct.agent_name,
		EXISTS(SELECT 1 FROM bud_proposals p WHERE p.task_id=q.task_id),
		EXISTS(SELECT 1 FROM turn_skills s WHERE s.task_id=q.task_id AND s.name='create-bud')
		FROM choice_cards q JOIN chat_turns ct ON ct.task_id=q.task_id
		JOIN messages m ON m.task_id=q.task_id AND m.role='user'
		LEFT JOIN turn_details td ON td.task_id=q.task_id
		WHERE COALESCE(td.origin,'chat')<>?
		AND NOT EXISTS(SELECT 1 FROM bud_choice_cards b WHERE b.task_id=q.task_id)
		AND NOT EXISTS(SELECT 1 FROM github_write_cards g WHERE g.task_id=q.task_id)
		AND NOT EXISTS(SELECT 1 FROM management_cards p WHERE p.task_id=q.task_id)
		AND NOT EXISTS(SELECT 1 FROM turn_details child WHERE child.parent_id=q.answer_task_id AND child.origin='handoff')
		AND NOT EXISTS(SELECT 1 FROM assignment_reuses r WHERE r.request_key=? || q.answer_task_id)`,
		backgroundRequest, namespacedKey(BackgroundWork, "handoff:"))
	if err != nil {
		return err
	}
	defer rows.Close()
	var cards []string
	for rows.Next() {
		var id, text, speaker string
		var draft, skill bool
		if err := rows.Scan(&id, &text, &speaker, &draft, &skill); err != nil {
			return err
		}
		if version < 2 && (draft || skill || BudDraftRequest(text, speaker) && !BudCreationFollowup(text)) {
			cards = append(cards, id)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, id := range cards {
		if _, err := tx.ExecContext(ctx, "INSERT INTO bud_choice_cards VALUES (?)", id); err != nil {
			return err
		}
	}
	// Follow the selected answer into subsequent legacy choice cards. Start
	// with the markers already written by an earlier upgrade as well.
	if _, err := tx.ExecContext(ctx, `WITH RECURSIVE bud_questions(id) AS (
		SELECT task_id FROM bud_choice_cards
		UNION SELECT next.task_id FROM bud_questions b
		JOIN choice_cards parent ON parent.task_id=b.id
		JOIN choice_cards next ON next.task_id=parent.answer_task_id
		LEFT JOIN turn_details td ON td.task_id=next.task_id
		WHERE COALESCE(td.origin,'chat')<>?
		AND NOT EXISTS(SELECT 1 FROM github_write_cards g WHERE g.task_id=next.task_id)
		AND NOT EXISTS(SELECT 1 FROM management_cards m WHERE m.task_id=next.task_id)
		AND NOT EXISTS(SELECT 1 FROM turn_details child WHERE child.parent_id=next.answer_task_id AND child.origin='handoff')
		AND NOT EXISTS(SELECT 1 FROM assignment_reuses r WHERE r.request_key=? || next.answer_task_id)
	) INSERT OR IGNORE INTO bud_choice_cards SELECT id FROM bud_questions`, backgroundRequest, namespacedKey(BackgroundWork, "handoff:")); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "PRAGMA user_version=3"); err != nil {
		return err
	}
	return tx.Commit()
}

func saveBudChoice(ctx context.Context, tx *sql.Tx, id string, card ChoiceCard) error {
	if err := saveChoiceCard(ctx, tx, id, card); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO bud_choice_cards VALUES (?)", id)
	return err
}

type BudApproval string

const (
	CreateBudOnly     BudApproval = "create_only"
	CreateBudWithWork BudApproval = "start_work"
)

type BudApprovalConflict struct{ Previous BudApproval }

func (e *BudApprovalConflict) Error() string {
	if e.Previous == CreateBudOnly {
		return "This Bud was created separately. Ask the Bud to start the work in chat."
	}
	return "This Bud was already created and its saved work was started."
}

func (s *Store) CreateProposedBud(ctx context.Context, chat, task string, approval BudApproval) (Agent, error) {
	if approval != "" && approval != CreateBudOnly && approval != CreateBudWithWork {
		return Agent{}, &InputError{"Choose how to create this Bud."}
	}
	tx, err := s.beginMobileWrite(ctx)
	if err != nil {
		return Agent{}, err
	}
	defer tx.Rollback()
	if err := requireOpenConversation(ctx, tx, chat); err != nil {
		return Agent{}, err
	}
	var id string
	var profile Agent
	err = tx.QueryRowContext(ctx, `SELECT p.bud_id,p.name,p.role,p.instructions FROM bud_proposals p
		JOIN chat_turns ct ON ct.task_id=p.task_id WHERE p.task_id=? AND ct.conversation_id=?`, task, chat).
		Scan(&id, &profile.Name, &profile.Role, &profile.Instructions)
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, ErrNotFound
	}
	if err != nil {
		return Agent{}, err
	}
	if saved, err := scanAgent(tx.QueryRowContext(ctx, "SELECT "+agentFields+" FROM agents a"+agentSettings+" WHERE a.id=?", id)); !errors.Is(err, ErrNotFound) {
		if err != nil {
			return Agent{}, err
		}
		if saved.Removed {
			return Agent{}, ErrNotFound
		}
		if approval != "" {
			var previous BudApproval
			// Drafts approved by older servers already started their saved work.
			err := tx.QueryRowContext(ctx, `SELECT COALESCE(
				(SELECT mode FROM bud_proposal_approvals WHERE task_id=?),
				(SELECT 'start_work' FROM bud_proposal_work WHERE task_id=?), 'create_only')`, task, task).Scan(&previous)
			if err != nil {
				return Agent{}, err
			}
			if previous != approval {
				return Agent{}, &BudApprovalConflict{Previous: previous}
			}
		}
		saved.ConversationID = "conv_" + strings.TrimPrefix(id, "agt_")
		return saved, nil
	}
	var needsRevision bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM bud_choice_cards WHERE task_id=?)`, task).Scan(&needsRevision); err != nil {
		return Agent{}, err
	}
	if needsRevision {
		return Agent{}, &InputError{"Answer the choice and use the revised Bud draft."}
	}
	var archivedCall bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM voice_call_turns vc JOIN archived_calls a
		ON a.call_id=vc.call_id WHERE vc.task_id=?)`, task).Scan(&archivedCall); err != nil {
		return Agent{}, err
	}
	if archivedCall {
		return Agent{}, &InputError{"Restore the call before creating its Bud draft."}
	}
	var data, earlier string
	err = tx.QueryRowContext(ctx, "SELECT handoffs,earlier_attachment_source FROM bud_proposal_work WHERE task_id=?", task).Scan(&data, &earlier)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Agent{}, err
	}
	if data != "" && approval == "" {
		return Agent{}, &InputError{"Update Buddy to review the saved work and choose how to create this Bud."}
	}
	if data == "" && approval == CreateBudWithWork {
		return Agent{}, &InputError{"This draft has no saved assignments. Choose Create Bud to save the profile."}
	}
	saved, err := s.saveAgentInTx(ctx, tx, id, profile, true, true)
	if err != nil {
		return Agent{}, err
	}
	if data != "" && approval == CreateBudWithWork {
		var handoffs []Handoff
		if err := json.Unmarshal([]byte(data), &handoffs); err != nil {
			return Agent{}, err
		}
		if _, _, err := queueBudProposalWork(ctx, tx, task, saved.ID, handoffs, earlier); err != nil {
			if errors.Is(err, ErrQueueFull) {
				return Agent{}, &InputError{"The work queue is full. Choose Create Bud only or try again later."}
			}
			return Agent{}, err
		}
	}
	mode := CreateBudOnly
	if data != "" {
		mode = approval
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO bud_proposal_approvals VALUES (?,?)", task, mode); err != nil {
		return Agent{}, err
	}
	var message int64
	if err := tx.QueryRowContext(ctx, "SELECT id FROM messages WHERE task_id=? AND role='assistant'", task).Scan(&message); err != nil {
		return Agent{}, err
	}
	if err := bumpMessageVersion(ctx, tx, chat, message); err != nil {
		return Agent{}, err
	}
	if err := s.commitProposedBud(tx, saved); err != nil {
		var provision *budProvisionError
		if errors.As(err, &provision) {
			return Agent{}, &InputError{"The Bud’s workspace or profile folder is unavailable. Try again once it’s available."}
		}
		return Agent{}, err
	}
	return saved, nil
}

// Provision after queue validation. If provisioning or the commit fails, only
// remove empty directories and the generated profile that did not exist before.
func (s *Store) commitProposedBud(tx *sql.Tx, bud Agent) error {
	var created []string
	committed := false
	defer func() {
		if !committed {
			for i := len(created) - 1; i >= 0; i-- {
				if err := os.Remove(created[i]); err != nil && !errors.Is(err, os.ErrNotExist) {
					log.Printf("clean up Bud provisioning: %v", err)
				}
			}
		}
	}()
	provisionError := func(err error) error {
		log.Printf("provision Bud %s: %v", bud.ID, err)
		return &budProvisionError{err}
	}
	if s.workspaceRoot != "" {
		err := os.Mkdir(bud.Workspace, 0700)
		if err == nil {
			created = append(created, bud.Workspace)
		} else if !errors.Is(err, os.ErrExist) {
			return provisionError(err)
		}
		if _, err := s.managedWorkspace(bud.ID); err != nil {
			return provisionError(err)
		}
	}
	if s.profilesRoot != "" {
		dir := filepath.Join(s.profilesRoot, bud.ID)
		err := os.Mkdir(dir, 0700)
		if err == nil {
			created = append(created, dir, filepath.Join(dir, "AGENTS.md"))
		} else if !errors.Is(err, os.ErrExist) {
			return provisionError(err)
		}
		if _, err := s.personality(bud.ID); err != nil {
			return provisionError(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func queueBudProposalWork(ctx context.Context, tx *sql.Tx, task, bud string, handoffs []Handoff, earlier string) ([]AssignedWork, string, error) {
	handoffs = slices.Clone(handoffs)
	for i := range handoffs {
		if strings.TrimSpace(handoffs[i].AgentID) == newBudAssignee {
			handoffs[i].AgentID = bud
		}
	}
	return queueChatHandoffs(ctx, tx, task, "bud-approval:"+task, handoffs, earlier, true, bud)
}

func (s *Store) saveChatBud(ctx context.Context, tx *sql.Tx, task string, draft BudDraft) (Agent, string, error) {
	profile, err := saveBudProposal(ctx, tx, task, draft)
	if err != nil || !draft.CreateNow {
		return Agent{}, "", err
	}
	var request, speaker string
	err = tx.QueryRowContext(ctx, `SELECT m.text,ct.agent_name FROM messages m JOIN chat_turns ct ON ct.task_id=m.task_id
		WHERE m.task_id=? AND m.role='user' AND NOT EXISTS(
		SELECT 1 FROM turn_skills sk WHERE sk.task_id=m.task_id AND sk.name='create-bud')
		AND NOT EXISTS(SELECT 1 FROM choice_card_answers q WHERE q.answer_task_id=m.task_id)`, task).Scan(&request, &speaker)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !authorizesBudCreation(request, draft.Evidence, speaker, profile.Name) {
		return Agent{}, "", nil
	}
	if err != nil {
		return Agent{}, "", err
	}
	return s.provisionProposedBud(ctx, tx, profile)
}
