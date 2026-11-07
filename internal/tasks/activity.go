package tasks

import "strings"

type ActivityKind string

const (
	Listening   ActivityKind = "listening"
	Starting    ActivityKind = "starting"
	Reasoning   ActivityKind = "reasoning"
	Reading     ActivityKind = "reading"
	Searching   ActivityKind = "searching"
	Editing     ActivityKind = "editing"
	RunningTool ActivityKind = "running"
	Replying    ActivityKind = "replying"
	Stopping    ActivityKind = "stopping"
)

var activityLabels = map[ActivityKind]string{
	Starting:    "Starting Codex…",
	Listening:   "Waiting for the model…",
	Reasoning:   "Working through the request…",
	Reading:     "Looking through files…",
	Searching:   "Looking something up…",
	Editing:     "Making changes…",
	RunningTool: "Running a tool…",
	Replying:    "Writing a reply…",
	Stopping:    "Stopping…",
}

func activityLabel(kind ActivityKind, detail string) string {
	label := activityLabels[kind]
	if detail != "" {
		return strings.TrimSuffix(label, "…") + " · " + detail
	}
	return label
}

func activityKind(label string) ActivityKind {
	for kind, text := range activityLabels {
		if text == label || strings.HasPrefix(label, strings.TrimSuffix(text, "…")+" · ") {
			return kind
		}
	}
	return Listening
}

func DeviceActivity(label string) ActivityKind { return activityKind(label) }
