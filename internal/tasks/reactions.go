package tasks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
)

const reactionSchema = `
CREATE TABLE IF NOT EXISTS message_reactions (
	message_id INTEGER NOT NULL REFERENCES messages(id),
	actor TEXT NOT NULL,
	emoji TEXT NOT NULL,
	PRIMARY KEY(message_id,actor)
);
CREATE TABLE IF NOT EXISTS reaction_versions (
	message_id INTEGER PRIMARY KEY REFERENCES messages(id),
	conversation_id TEXT NOT NULL REFERENCES conversations(id),
	revision INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS reaction_versions_chat ON reaction_versions(conversation_id,revision);
`

type ReactionOption struct {
	Emoji string `json:"emoji"`
	Label string `json:"label"`
}

var reactionOptions = []ReactionOption{
	{"👍", "Thumbs up"}, {"❤️", "Heart"}, {"😂", "Laugh"}, {"🎉", "Celebrate"},
	{"👀", "Eyes"}, {"🙏", "Thanks"}, {"✅", "Check mark"}, {"👎", "Thumbs down"},
}

type MessageReaction struct {
	Emoji     string `json:"emoji"`
	AgentID   string `json:"agent_id,omitempty"`
	AgentName string `json:"agent_name,omitempty"`
}

type ReactionUpdate struct {
	MessageID int64             `json:"message_id"`
	Revision  int64             `json:"revision"`
	Reactions []MessageReaction `json:"reactions"`
}

const reactionJSON = `(SELECT json_group_array(json_object('emoji',r.emoji,'agent_id',r.actor,'agent_name',COALESCE(a.name,'')))
	FROM message_reactions r LEFT JOIN agents a ON a.id=r.actor WHERE r.message_id=m.id)`

func validReaction(emoji string) bool {
	return slices.ContainsFunc(reactionOptions, func(option ReactionOption) bool { return option.Emoji == emoji })
}

// SetReaction sets the owner's one reaction on a Bud's message. The empty
// emoji removes it. Writing the same value again leaves the revision intact.
func (s *Store) SetReaction(ctx context.Context, chat string, message int64, emoji string) (ReactionUpdate, error) {
	if emoji != "" && !validReaction(emoji) {
		return ReactionUpdate{}, &InputError{"Choose an emoji from the reaction menu."}
	}
	tx, err := s.beginMobileWrite(ctx)
	if err != nil {
		return ReactionUpdate{}, err
	}
	defer tx.Rollback()
	if err := requireOpenConversation(ctx, tx, chat); err != nil {
		return ReactionUpdate{}, err
	}
	if _, err := conversationParticipant(ctx, tx, chat, ""); err != nil {
		return ReactionUpdate{}, err
	}
	var target int64
	err = tx.QueryRowContext(ctx, `SELECT m.id FROM messages m JOIN chat_turns ct ON ct.task_id=m.task_id
		JOIN tasks t ON t.id=m.task_id LEFT JOIN turn_details td ON td.task_id=t.id
		WHERE ct.conversation_id=? AND m.id=? AND m.role='assistant' AND `+visibleChatMessage, chat, message).Scan(&target)
	if errors.Is(err, sql.ErrNoRows) {
		return ReactionUpdate{}, ErrNotFound
	}
	if err != nil {
		return ReactionUpdate{}, err
	}
	if err := setMessageReaction(ctx, tx, chat, target, "", emoji); err != nil {
		return ReactionUpdate{}, err
	}
	update, err := scanReactionUpdate(tx.QueryRowContext(ctx, `SELECT m.id,COALESCE(v.revision,0),`+reactionJSON+
		` FROM messages m LEFT JOIN reaction_versions v ON v.message_id=m.id WHERE m.id=?`, target))
	if err != nil {
		return ReactionUpdate{}, err
	}
	return update, tx.Commit()
}

func setMessageReaction(ctx context.Context, tx *sql.Tx, chat string, message int64, actor, emoji string) error {
	var result sql.Result
	var err error
	if emoji == "" {
		result, err = tx.ExecContext(ctx, "DELETE FROM message_reactions WHERE message_id=? AND actor=?", message, actor)
	} else {
		result, err = tx.ExecContext(ctx, `INSERT INTO message_reactions VALUES (?,?,?)
			ON CONFLICT(message_id,actor) DO UPDATE SET emoji=excluded.emoji WHERE emoji<>excluded.emoji`, message, actor, emoji)
	}
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil || changed == 0 {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO reaction_versions VALUES (?,?,(
		SELECT COALESCE(max(revision),0)+1 FROM reaction_versions WHERE conversation_id=?))
		ON CONFLICT(message_id) DO UPDATE SET revision=excluded.revision`, message, chat, chat)
	return err
}

func saveBudReaction(ctx context.Context, tx *sql.Tx, task, emoji string) (bool, error) {
	if !validReaction(emoji) {
		return false, nil
	}
	var chat, bud string
	var message int64
	err := tx.QueryRowContext(ctx, `SELECT ct.conversation_id,td.agent_id,m.id
		FROM messages m JOIN chat_turns ct ON ct.task_id=m.task_id JOIN turn_details td ON td.task_id=m.task_id
		WHERE m.task_id=? AND m.role='user' AND td.origin='chat'
		AND NOT EXISTS(SELECT 1 FROM choice_cards c WHERE c.answer_task_id=m.task_id)
		AND NOT EXISTS(SELECT 1 FROM work_questions w WHERE w.task_id=m.task_id)
		AND NOT EXISTS(SELECT 1 FROM archived_groups a WHERE a.conversation_id=ct.conversation_id)
		AND NOT EXISTS(SELECT 1 FROM removed_buds r WHERE r.agent_id=td.agent_id)`, task).Scan(&chat, &bud, &message)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, setMessageReaction(ctx, tx, chat, message, bud, emoji)
}

func scanReactionUpdate(row scanner) (ReactionUpdate, error) {
	var update ReactionUpdate
	var encoded string
	if err := row.Scan(&update.MessageID, &update.Revision, &encoded); err != nil {
		return update, err
	}
	return update, json.Unmarshal([]byte(encoded), &update.Reactions)
}

func reactionUpdates(ctx context.Context, tx *sql.Tx, chat string, after, before int64) ([]ReactionUpdate, error) {
	rows, err := tx.QueryContext(ctx, `SELECT m.id,v.revision,`+reactionJSON+`
		FROM reaction_versions v JOIN messages m ON m.id=v.message_id JOIN tasks t ON t.id=m.task_id
		LEFT JOIN turn_details td ON td.task_id=t.id WHERE v.conversation_id=? AND v.revision>? AND m.id<?
		AND `+visibleChatMessage+` ORDER BY v.revision`, chat, after, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var updates []ReactionUpdate
	for rows.Next() {
		update, err := scanReactionUpdate(rows)
		if err != nil {
			return nil, err
		}
		updates = append(updates, update)
	}
	return updates, rows.Err()
}
