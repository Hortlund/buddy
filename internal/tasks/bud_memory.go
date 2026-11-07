package tasks

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const budMemorySchema = `
CREATE TABLE IF NOT EXISTS bud_souls (
	agent_id TEXT PRIMARY KEY REFERENCES agents(id),
	body TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS bud_learned_memory (
	agent_id TEXT NOT NULL REFERENCES agents(id),
	topic TEXT NOT NULL,
	note TEXT NOT NULL,
	updated INTEGER NOT NULL,
	PRIMARY KEY(agent_id,topic)
);
CREATE TABLE IF NOT EXISTS bud_learning_disabled (
	agent_id TEXT PRIMARY KEY REFERENCES agents(id)
);
`

func initialSoul(name, role, instructions string) string {
	if role == "" {
		role = "Personal companion"
	}
	if instructions = managementExcerpt(instructions, 400); instructions == "" {
		instructions = "Be curious and practical. Learn how the user likes to work, and follow through on what you take on."
	}
	return "# Soul\n\nI’m " + name + ", your " + role + ".\n\n## How I work\n\n" + instructions + "\n"
}

type BudDocuments struct {
	Soul     string `json:"soul"`
	Revision string `json:"revision"`
	Memory   string `json:"memory"`
	Learning bool   `json:"learning"`
}

type LearnedNote struct {
	Topic    string `json:"topic"`
	Note     string `json:"note"`
	Evidence string `json:"evidence"`
}

var memoryTopic = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,47}$`)

func soulDocument(ctx context.Context, db queryer, bud string) (string, string, error) {
	var body string
	err := db.QueryRowContext(ctx, "SELECT body FROM bud_souls WHERE agent_id=?", bud).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		var name, role, instructions string
		err = db.QueryRowContext(ctx, `SELECT a.name,COALESCE(r.role,''),a.instructions
			FROM agents a LEFT JOIN agent_roles r ON r.agent_id=a.id WHERE a.id=?`, bud).Scan(&name, &role, &instructions)
		if err != nil {
			return "", "", err
		}
		body = initialSoul(name, role, instructions)
	}
	return body, documentRevision(body), err
}

func documentRevision(body string) string {
	hash := sha256.Sum256([]byte(body))
	return hex.EncodeToString(hash[:])
}

func learningEnabled(ctx context.Context, db queryer, bud string) (bool, error) {
	var enabled bool
	err := db.QueryRowContext(ctx, "SELECT NOT EXISTS(SELECT 1 FROM bud_learning_disabled WHERE agent_id=?)", bud).Scan(&enabled)
	return enabled, err
}

func learnedDocument(ctx context.Context, db queryer, bud string) (string, error) {
	rows, err := db.QueryContext(ctx, "SELECT topic,note FROM bud_learned_memory WHERE agent_id=? ORDER BY updated DESC,topic", bud)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var doc strings.Builder
	for rows.Next() {
		var topic, note string
		if err := rows.Scan(&topic, &note); err != nil {
			return "", err
		}
		if doc.Len() == 0 {
			doc.WriteString("# Memory\n\n")
		}
		doc.WriteString("- `" + topic + "` — " + note + "\n")
	}
	return doc.String(), rows.Err()
}

func (s *Store) BudDocuments(ctx context.Context, bud string) (BudDocuments, error) {
	var out BudDocuments
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if _, err := agentRemoved(ctx, tx, bud); err != nil {
		return out, err
	}
	out.Soul, out.Revision, err = soulDocument(ctx, tx, bud)
	if err == nil {
		out.Memory, err = learnedDocument(ctx, tx, bud)
	}
	if err == nil {
		out.Learning, err = learningEnabled(ctx, tx, bud)
	}
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}

func (s *Store) SaveSoul(ctx context.Context, bud, body, revision string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := saveSoul(ctx, tx, bud, body, revision); err != nil {
		return err
	}
	return tx.Commit()
}

func saveSoul(ctx context.Context, tx *sql.Tx, bud, body, revision string) error {
	if len(body) > 8000 || !utf8.ValidString(body) || strings.ContainsRune(body, 0) {
		return &InputError{"Keep SOUL.md under 8 KB of UTF-8 Markdown."}
	}
	removed, err := agentRemoved(ctx, tx, bud)
	if err != nil {
		return err
	}
	if removed {
		return &InputError{"This Bud was removed."}
	}
	_, current, err := soulDocument(ctx, tx, bud)
	if err != nil {
		return err
	}
	if revision != current {
		return &InputError{"SOUL.md changed. Reload it before saving your edits."}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO bud_souls VALUES (?,?) ON CONFLICT(agent_id)
		DO UPDATE SET body=excluded.body`, bud, body)
	return err
}

func (s *Store) SetBudLearning(ctx context.Context, bud string, enabled bool) error {
	if _, err := agentRemoved(ctx, s.db, bud); err != nil {
		return err
	}
	statement := "INSERT OR IGNORE INTO bud_learning_disabled VALUES (?)"
	if enabled {
		statement = "DELETE FROM bud_learning_disabled WHERE agent_id=?"
	}
	_, err := s.db.ExecContext(ctx, statement, bud)
	return err
}

func (s *Store) ClearLearnedMemory(ctx context.Context, bud string) error {
	if _, err := agentRemoved(ctx, s.db, bud); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, "DELETE FROM bud_learned_memory WHERE agent_id=?", bud)
	return err
}

func privateMemoryTurn(ctx context.Context, db queryer, task string, automatic bool) (string, string, error) {
	var bud, message string
	err := db.QueryRowContext(ctx, `SELECT td.agent_id,m.text FROM turn_details td
		JOIN chat_turns ct ON ct.task_id=td.task_id JOIN messages m ON m.task_id=td.task_id AND m.role='user'
		WHERE td.task_id=? AND td.origin='chat'
		AND NOT EXISTS(SELECT 1 FROM group_chats g WHERE g.conversation_id=ct.conversation_id)
		AND NOT EXISTS(SELECT 1 FROM work_questions w WHERE w.task_id=td.task_id)
		AND NOT EXISTS(SELECT 1 FROM choice_cards c WHERE c.answer_task_id=td.task_id)
		AND NOT EXISTS(SELECT 1 FROM chat_attachments f WHERE f.task_id=td.task_id)
		AND NOT EXISTS(SELECT 1 FROM turn_skills k WHERE k.task_id=td.task_id)
		AND (NOT ? OR NOT EXISTS(SELECT 1 FROM bud_learning_disabled d WHERE d.agent_id=td.agent_id))
		AND NOT EXISTS(SELECT 1 FROM removed_buds r WHERE r.agent_id=td.agent_id)`, task, automatic).Scan(&bud, &message)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return bud, message, err
}

func userTurnEvidence(message, evidence string) bool {
	message = strings.TrimSpace(message)
	evidence = strings.TrimSpace(evidence)
	return len(evidence) >= 6 && strings.Contains(message, evidence) &&
		!strings.HasPrefix(message, ">") && !strings.HasPrefix(message, "\"") && !strings.HasPrefix(message, "“") &&
		!strings.Contains(message, "```") && !strings.Contains(message, "\n>")
}

func memoryEvidence(message, evidence string) bool {
	if !userTurnEvidence(message, evidence) {
		return false
	}
	lower := strings.ToLower(message)
	for _, phrase := range []string{"don't remember", "don’t remember", "do not remember", "don't save", "don’t save", "do not save", "spara inte"} {
		if strings.Contains(lower, phrase) {
			return false
		}
	}
	request := normalizeManagementRequest(message)
	return !strings.HasPrefix(request, "forget ") && !strings.HasPrefix(request, "glöm ")
}

func saveLearning(ctx context.Context, tx *sql.Tx, task string, notes []LearnedNote) error {
	if len(notes) == 0 || len(notes) > 3 {
		return nil
	}
	bud, message, err := privateMemoryTurn(ctx, tx, task, true)
	if err != nil || bud == "" {
		return err
	}
	for _, learned := range notes {
		note := strings.TrimSpace(learned.Note)
		if !memoryTopic.MatchString(learned.Topic) || !validTitle(note, 400) ||
			!memoryEvidence(message, learned.Evidence) {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO bud_learned_memory VALUES (?,?,?,?) ON CONFLICT(agent_id,topic)
			DO UPDATE SET note=excluded.note,updated=excluded.updated`, bud, learned.Topic, note, time.Now().UnixMicro()); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM bud_learned_memory WHERE agent_id=? AND topic IN
		(SELECT topic FROM bud_learned_memory WHERE agent_id=? ORDER BY updated DESC,topic LIMIT -1 OFFSET 24)`, bud, bud)
	return err
}
