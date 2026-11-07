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
`

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
	var question, options, target string
	var answer sql.NullString
	var selected sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT cc.question,cc.options,COALESCE(td.agent_id,c.agent_id),cc.answer_task_id,cc.choice
		FROM choice_cards cc JOIN chat_turns ct ON ct.task_id=cc.task_id JOIN conversations c ON c.id=ct.conversation_id
		LEFT JOIN turn_details td ON td.task_id=ct.task_id WHERE cc.task_id=? AND ct.conversation_id=?`, cardID, conversation).
		Scan(&question, &options, &target, &answer, &selected)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	if err != nil {
		return Task{}, false, err
	}
	if answer.Valid {
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
	t, err := enqueueTurn(ctx, tx, AgentTurn, "\x00card:"+cardID, conversation, agent, workRequest{origin: "chat"})
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
	if _, err := tx.ExecContext(ctx, "UPDATE choice_cards SET answer_task_id=?,choice=? WHERE task_id=?", t.ID, choice, cardID); err != nil {
		return Task{}, false, err
	}
	return t, true, tx.Commit()
}
