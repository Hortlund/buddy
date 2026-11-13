package tasks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
)

const cardsSchema = `
CREATE TABLE IF NOT EXISTS choice_cards (
	task_id TEXT PRIMARY KEY REFERENCES chat_turns(task_id),
	question TEXT NOT NULL,
	options TEXT NOT NULL,
	answer_task_id TEXT UNIQUE REFERENCES chat_turns(task_id),
	choice INTEGER
);
CREATE TABLE IF NOT EXISTS choice_card_details (
	task_id TEXT PRIMARY KEY REFERENCES choice_cards(task_id),
	detail TEXT NOT NULL,
	option_details TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS choice_card_answers (
	answer_task_id TEXT PRIMARY KEY REFERENCES chat_turns(task_id),
	question_task_id TEXT NOT NULL REFERENCES choice_cards(task_id)
);
CREATE TABLE IF NOT EXISTS bud_choice_cards (
	task_id TEXT PRIMARY KEY REFERENCES choice_cards(task_id)
);
INSERT OR IGNORE INTO choice_card_answers
	SELECT answer_task_id,task_id FROM choice_cards WHERE answer_task_id IS NOT NULL;
CREATE TABLE IF NOT EXISTS background_clarifications (
	task_id TEXT PRIMARY KEY REFERENCES chat_turns(task_id),
	answer_task_id TEXT UNIQUE REFERENCES chat_turns(task_id)
);
CREATE TABLE IF NOT EXISTS background_clarification_answers (
	answer_task_id TEXT PRIMARY KEY REFERENCES chat_turns(task_id),
	question_task_id TEXT NOT NULL REFERENCES background_clarifications(task_id)
);
CREATE INDEX IF NOT EXISTS background_answers_question ON background_clarification_answers(question_task_id);
INSERT OR IGNORE INTO background_clarification_answers
	SELECT answer_task_id,task_id FROM background_clarifications WHERE answer_task_id IS NOT NULL;
`

const chatQuestionLinks = `SELECT question_task_id AS task_id,answer_task_id FROM choice_card_answers
	UNION ALL SELECT question_task_id,answer_task_id FROM background_clarification_answers`

const questionRequestChain = `WITH RECURSIVE question_links(task_id,answer_task_id) AS (` + chatQuestionLinks + `), requests(id) AS (
	VALUES (?)
	UNION SELECT q.task_id FROM question_links q JOIN requests r ON q.answer_task_id=r.id
)`

// A failed conversational choice can be retried. Confirmations that may have
// already performed a write keep their original receipt.
const retryableChoice = `EXISTS(SELECT 1 FROM tasks answer WHERE answer.id=q.answer_task_id
	AND (answer.status IN ('failed','cancelled','interrupted') OR (answer.status='succeeded'
		AND EXISTS(SELECT 1 FROM bud_choice_cards b WHERE b.task_id=q.task_id)
		AND NOT EXISTS(SELECT 1 FROM bud_proposals p WHERE p.task_id=answer.id)
		AND NOT EXISTS(SELECT 1 FROM choice_cards next WHERE next.task_id=answer.id))))
	AND NOT EXISTS(SELECT 1 FROM github_write_cards p WHERE p.task_id=q.task_id)
	AND NOT EXISTS(SELECT 1 FROM management_cards p WHERE p.task_id=q.task_id)`

const openBackgroundQuestion = `NOT EXISTS (
	SELECT 1 FROM background_clarification_answers a JOIN tasks answer ON answer.id=a.answer_task_id
	WHERE a.question_task_id=q.task_id AND answer.status IN ('queued','running','cancelling','succeeded')
)`

func backgroundQuestions(ctx context.Context, db queryer, chat string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT q.task_id FROM background_clarifications q
		JOIN chat_turns ct ON ct.task_id=q.task_id WHERE ct.conversation_id=? AND `+openBackgroundQuestion, chat)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func saveBackgroundQuestion(ctx context.Context, tx *sql.Tx, task string) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO background_clarifications(task_id) VALUES (?)", task)
	return err
}

type ChoiceCard struct {
	Confirmation  string   `json:"confirmation,omitempty"`
	Question      string   `json:"question"`
	Detail        string   `json:"detail,omitempty"`
	OptionDetails []string `json:"option_details,omitempty"`
	Options       []string `json:"options"`
	Choice        *int     `json:"choice,omitempty"`
}

const confirmationDetailLimit = 8000

func saveChoiceCard(ctx context.Context, tx *sql.Tx, id string, card ChoiceCard) error {
	card.Question = strings.TrimSpace(card.Question)
	if card.Question == "" || len(card.Question) > 500 || len(card.Options) < 2 || len(card.Options) > 5 {
		return &InputError{"A question needs 2 to 5 short answers."}
	}
	options := make([]string, 0, len(card.Options))
	for _, option := range card.Options {
		option = strings.TrimSpace(option)
		if !validTitle(option, 100) || slices.Contains(options, option) {
			return &InputError{"Use distinct answers on one line each."}
		}
		options = append(options, option)
	}
	detail := strings.TrimSpace(card.Detail)
	notes := slices.Clone(card.OptionDetails)
	maxDetail := 1000
	if card.Confirmation != "" {
		maxDetail = confirmationDetailLimit
	}
	if len(detail) > maxDetail || len(notes) != 0 && len(notes) != len(options) {
		return &InputError{"Keep card context short and supply one description per answer."}
	}
	for i := range notes {
		notes[i] = strings.TrimSpace(notes[i])
		if len(notes[i]) > 280 {
			return &InputError{"Keep answer descriptions under 280 bytes."}
		}
	}
	encoded, err := json.Marshal(options)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO choice_cards(task_id,question,options) VALUES (?,?,?)", id, card.Question, string(encoded))
	if err != nil || detail == "" && len(notes) == 0 {
		return err
	}
	encoded, err = json.Marshal(notes)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO choice_card_details VALUES (?,?,?)", id, detail, string(encoded))
	return err
}

// AnswerCard binds the answer to the original question and agent, including
// when two clients answer at the same time or a client retries a click.
func (s *Store) AnswerCard(ctx context.Context, conversation, cardID string, choice int) (Task, bool, error) {
	tx, err := s.beginMobileWrite(ctx)
	if err != nil {
		return Task{}, false, err
	}
	defer tx.Rollback()
	var options, target, origin string
	var answer sql.NullString
	var selected sql.NullInt64
	var retry bool
	err = tx.QueryRowContext(ctx, `SELECT q.options,COALESCE(td.agent_id,c.agent_id),q.answer_task_id,q.choice,COALESCE(td.origin,'chat'),`+retryableChoice+`
		FROM choice_cards q JOIN chat_turns ct ON ct.task_id=q.task_id JOIN conversations c ON c.id=ct.conversation_id
		LEFT JOIN turn_details td ON td.task_id=ct.task_id WHERE q.task_id=? AND ct.conversation_id=?`, cardID, conversation).
		Scan(&options, &target, &answer, &selected, &origin, &retry)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	if err != nil {
		return Task{}, false, err
	}
	if answer.Valid && !retry {
		if selected.Int64 != int64(choice) {
			return Task{}, false, ErrConflict
		}
		t, err := scanTask(tx.QueryRowContext(ctx, "SELECT "+fields+" FROM tasks WHERE id=?", answer.String))
		return t, false, err
	}
	if err := requireOpenConversation(ctx, tx, conversation); err != nil {
		return Task{}, false, err
	}
	var hidden bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM voice_call_turns vc JOIN archived_calls ac
		ON ac.call_id=vc.call_id WHERE vc.task_id=?)`, cardID).Scan(&hidden); err != nil {
		return Task{}, false, err
	}
	if hidden {
		return Task{}, false, &InputError{"Restore the call before answering this card."}
	}
	var labels []string
	if err := json.Unmarshal([]byte(options), &labels); err != nil {
		return Task{}, false, err
	}
	if choice < 0 || choice >= len(labels) {
		return Task{}, false, &InputError{"Choose an answer on the card."}
	}
	agent, err := conversationParticipant(ctx, tx, conversation, target)
	if err != nil {
		return Task{}, false, err
	}
	if err := checkQueue(ctx, tx, false); err != nil {
		return Task{}, false, err
	}
	if origin != backgroundRequest {
		origin = "chat"
	}
	key := "\x00card:" + cardID
	if retry {
		key += ":" + answer.String
	}
	t, err := enqueueTurn(ctx, tx, AgentTurn, key, conversation, agent, workRequest{origin: origin, backgroundQuestion: cardID})
	if err != nil {
		return Task{}, false, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO messages(task_id,role,text,created) VALUES (?,'user',?,?)",
		t.ID, labels[choice], t.CreatedAt.UnixMicro()); err != nil {
		return Task{}, false, err
	}
	if err := continueSkill(ctx, tx, t.ID, cardID); err != nil {
		return Task{}, false, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO choice_card_answers VALUES (?,?)", t.ID, cardID); err != nil {
		return Task{}, false, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE choice_cards SET answer_task_id=?,choice=? WHERE task_id=?", t.ID, choice, cardID); err != nil {
		return Task{}, false, err
	}
	return t, true, tx.Commit()
}
