package tasks

import (
	"slices"
	"time"
)

type Status string

const (
	Queued      Status = "queued"
	Running     Status = "running"
	NeedsInput  Status = "needs_input"
	Cancelling  Status = "cancelling"
	Succeeded   Status = "succeeded"
	Failed      Status = "failed"
	Cancelled   Status = "cancelled"
	Interrupted Status = "interrupted"
	RepoCheck          = "repo_check"
)

func (s Status) Active() bool {
	return s == Queued || s == Running || s == Cancelling
}

type Request struct {
	Kind           string   `json:"kind,omitempty"`
	Repository     string   `json:"repository"`
	Command        []string `json:"command"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
}

func (r Request) equal(other Request) bool {
	return r.Kind == other.Kind && r.Repository == other.Repository &&
		r.TimeoutSeconds == other.TimeoutSeconds && slices.Equal(r.Command, other.Command)
}

type Task struct {
	ID string `json:"id"`
	Request
	Status     Status     `json:"status"`
	Summary    string     `json:"summary"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

type Event struct {
	Seq    int64     `json:"seq"`
	At     time.Time `json:"at"`
	Stream string    `json:"stream"`
	Text   string    `json:"text"`
}
