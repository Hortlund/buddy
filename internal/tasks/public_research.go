package tasks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
)

type researchRequest struct {
	User   string               `json:"user,omitempty"`
	Choice *authorizationChoice `json:"choice,omitempty"`
}

// Previously scheduled routines stored an array of plain user utterances.
func (r *researchRequest) UnmarshalJSON(data []byte) error {
	if len(data) != 0 && data[0] == '"' {
		return json.Unmarshal(data, &r.User)
	}
	type request researchRequest
	return json.Unmarshal(data, (*request)(r))
}

type researchSubject struct {
	Name     string `json:"name,omitempty"`
	Minutes  int    `json:"minutes,omitempty"`
	Position int    `json:"position,omitempty"`
}

type publicResearchContext struct {
	Requests []researchRequest `json:"requests"`
	Subject  researchSubject   `json:"routine,omitzero"`
}

func plainResearchRequests(lines []string) []researchRequest {
	requests := make([]researchRequest, 0, len(lines))
	for i := len(lines) - 1; i >= 0; i-- {
		requests = append(requests, researchRequest{User: lines[i]})
	}
	return requests
}

// Retain the originating request for a choice even when it is older than the
// recent transcript, and keep applied corrections attached to each turn.
func readPublicRequestHistory(ctx context.Context, db queryer, conversation, task string, includeCurrentSteering bool) ([]researchRequest, error) {
	rows, err := db.QueryContext(ctx, questionRequestChain+`, recent AS (
		SELECT m.id,m.task_id,m.text FROM messages m JOIN chat_turns ct ON ct.task_id=m.task_id
		LEFT JOIN turn_details td ON td.task_id=m.task_id
		WHERE ct.conversation_id=? AND m.role='user' AND COALESCE(td.origin,'chat') IN ('chat','background_request')
		AND m.id<=(SELECT id FROM messages WHERE task_id=? AND role='user')
		ORDER BY m.id DESC LIMIT 8
	), relevant AS (
		SELECT id,task_id,text FROM recent
		UNION SELECT m.id,m.task_id,m.text FROM messages m JOIN requests r ON r.id=m.task_id
		JOIN chat_turns ct ON ct.task_id=m.task_id WHERE m.role='user' AND ct.conversation_id=?
	) SELECT text,question FROM (
		SELECT m.id,0 AS correction_id,m.text,COALESCE(
			(SELECT q.question FROM choice_card_answers a JOIN choice_cards q ON q.task_id=a.question_task_id WHERE a.answer_task_id=m.task_id),
			(SELECT p.text FROM background_clarification_answers a JOIN messages p ON p.task_id=a.question_task_id
			 WHERE a.answer_task_id=m.task_id AND p.role='assistant')) AS question
		FROM relevant m
		UNION ALL SELECT m.id,s.id,s.text,NULL FROM relevant m JOIN steering_messages s ON s.task_id=m.task_id
		WHERE s.state=? AND (? OR m.task_id<>?)
	) ORDER BY id,correction_id`, task, conversation, task, conversation, steerApplied, includeCurrentSteering, task)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanResearchRequests(rows)
}

func scanResearchRequests(rows *sql.Rows) ([]researchRequest, error) {
	var requests []researchRequest
	for rows.Next() {
		var text string
		var question sql.NullString
		if err := rows.Scan(&text, &question); err != nil {
			return nil, err
		}
		if question.Valid {
			requests = append(requests, researchRequest{Choice: &authorizationChoice{Question: question.String, Selected: text}})
		} else {
			requests = append(requests, researchRequest{User: text})
		}
	}
	return requests, rows.Err()
}

const researchAncestry = `WITH RECURSIVE ancestors(id,depth,before) AS (
	VALUES (?,0,NULL) UNION ALL SELECT td.parent_id,a.depth+1,t.created FROM ancestors a
	JOIN turn_details td ON td.task_id=a.id JOIN tasks t ON t.id=a.id WHERE td.parent_id IS NOT NULL AND a.depth<8
)`

func readWorkResearchAnswers(ctx context.Context, db queryer, task string) ([]researchRequest, error) {
	rows, err := db.QueryContext(ctx, researchAncestry+` SELECT a.text,q.text FROM ancestors parent
		JOIN work_inputs w ON w.job_id=parent.id
		JOIN messages q ON q.task_id=w.task_id AND q.role='assistant'
		JOIN messages a ON a.task_id=w.task_id AND a.role='user'
		WHERE w.answer_key IS NOT NULL AND trim(a.text)<>'' AND (parent.before IS NULL OR a.created<=parent.before)
		ORDER BY parent.depth DESC,a.id`, task)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanResearchRequests(rows)
}

// Existing skill routines retain an exact request link. A legacy voice
// routine can also be traced to its request when the call has one caller
// utterance. A generated routine prompt alone cannot supply that link.
func recoverRoutineResearch(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `WITH single_voice_request AS (
		SELECT vc.call_id,ct.conversation_id,min(m.text) AS request
		FROM voice_call_turns vc JOIN chat_turns ct ON ct.task_id=vc.task_id
		JOIN messages m ON m.task_id=vc.task_id AND m.role='user'
		GROUP BY vc.call_id,ct.conversation_id HAVING count(*)=1
	)
	SELECT r.id,r.conversation_id,r.name,r.minutes,COALESCE(s.task_id,''),COALESCE(v.request,'')
	FROM routines r LEFT JOIN routine_skill_sources s ON s.routine_id=r.id
	LEFT JOIN voice_call_routines vr ON vr.routine_id=r.id
	LEFT JOIN single_voice_request v ON v.call_id=vr.call_id AND v.conversation_id=r.conversation_id
	WHERE (s.routine_id IS NOT NULL OR v.request IS NOT NULL)
	AND NOT EXISTS(SELECT 1 FROM routine_public_requests p WHERE p.routine_id=r.id)`)
	if err != nil {
		return err
	}
	type source struct {
		routine           Routine
		chat, task, voice string
	}
	var sources []source
	for rows.Next() {
		var src source
		if err := rows.Scan(&src.routine.ID, &src.chat, &src.routine.Name, &src.routine.Minutes, &src.task, &src.voice); err != nil {
			rows.Close()
			return err
		}
		sources = append(sources, src)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, src := range sources {
		var requests []researchRequest
		if src.task != "" {
			requests, err = readPublicRequestHistory(ctx, tx, src.chat, src.task, true)
			if err != nil {
				return err
			}
		} else if strings.TrimSpace(src.voice) != "" {
			requests = []researchRequest{{User: src.voice}}
		}
		if err := saveRoutineResearch(ctx, tx, src.routine, 0, requests); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func backgroundPublicResearch(ctx context.Context, db queryer, chat, task string) (publicResearchContext, error) {
	var result publicResearchContext
	// A delegated child can reuse the original authenticated request. The
	// assignment's generated brief is never a source of public search terms.
	var raw, source, call string
	err := db.QueryRowContext(ctx, researchAncestry+`
		SELECT COALESCE(p.request,rp.request,''), t.id, COALESCE(vc.call_id,vt.call_id,'')
		FROM ancestors a JOIN tasks t ON t.id=a.id
		LEFT JOIN task_public_requests p ON p.task_id=t.id
		LEFT JOIN turn_details td ON td.task_id=t.id AND td.origin='routine'
		LEFT JOIN routine_public_requests rp ON rp.routine_id=td.routine_id
		LEFT JOIN voice_call_turns vc ON vc.task_id=t.id
		LEFT JOIN voice_team_steps vt ON vt.task_id=t.id
		WHERE p.task_id IS NOT NULL OR rp.routine_id IS NOT NULL OR vc.task_id IS NOT NULL OR vt.task_id IS NOT NULL OR
			EXISTS(SELECT 1 FROM messages m WHERE m.task_id=t.id AND m.role='user')
		ORDER BY a.depth LIMIT 1`, task).Scan(&raw, &source, &call)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return result, nil
	case err != nil:
		return result, err
	case raw != "":
		return decodeRoutineResearch(raw, researchSubject{}), nil
	case call == "":
		result.Requests, err = readPublicRequestHistory(ctx, db, chat, source, true)
		return result, err
	}
	// Older voice jobs lack a request snapshot. Use caller lines from the
	// same call that were already present when the assignment was queued.
	rows, err := db.QueryContext(ctx, `SELECT m.text FROM messages m JOIN voice_call_turns vc ON vc.task_id=m.task_id
		JOIN chat_turns ct ON ct.task_id=m.task_id JOIN tasks t ON t.id=m.task_id
		WHERE vc.call_id=? AND ct.conversation_id=? AND m.role='user'
		AND t.rowid<(SELECT rowid FROM tasks WHERE id=?) ORDER BY m.id DESC LIMIT 8`, call, chat, source)
	if err != nil {
		return result, err
	}
	lines, err := scanRequestLines(rows)
	result.Requests = plainResearchRequests(lines)
	return result, err
}

// Results belong to the authenticated request and the corrections applied to it.
func (turn Turn) publicSearch(
	resolve func(context.Context, publicResearchContext) (string, error), search func(context.Context, string) (string, error),
) func(context.Context) (string, error) {
	var mu sync.Mutex
	var cached bool
	var previous []string
	var result string
	return func(ctx context.Context) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		var corrections []string
		if turn.steering != nil && turn.steering.applied != nil {
			var err error
			corrections, err = turn.steering.applied(ctx)
			if err != nil {
				return "", err
			}
		}
		if cached && slices.Equal(previous, corrections) {
			return result, nil
		}
		request, research := turn.publicRequest, turn.publicResearch
		if len(corrections) != 0 {
			research.Requests = slices.Clone(research.Requests)
			if request != "" {
				research.Requests = append(research.Requests, researchRequest{User: request})
			}
			for _, correction := range corrections {
				research.Requests = append(research.Requests, researchRequest{User: correction})
			}
		}
		if len(research.Requests) != 0 {
			var err error
			request, err = resolve(ctx, research)
			if err != nil {
				return "", err
			}
		}
		answer, err := search(ctx, request)
		if err == nil {
			cached, previous, result = true, slices.Clone(corrections), answer
		}
		return answer, err
	}
}

func decodeRoutineResearch(raw string, subject researchSubject) publicResearchContext {
	var context publicResearchContext
	if json.Unmarshal([]byte(raw), &context) == nil && len(context.Requests) != 0 {
		return context
	}
	var requests []researchRequest
	if json.Unmarshal([]byte(raw), &requests) == nil && len(requests) != 0 {
		return publicResearchContext{Requests: requests, Subject: subject}
	}
	if strings.TrimSpace(raw) != "" {
		return publicResearchContext{Requests: []researchRequest{{User: raw}}, Subject: subject}
	}
	return publicResearchContext{}
}
