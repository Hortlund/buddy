package api

import (
	"regexp"
	"strings"
	"time"
	"unicode"

	"buddy/internal/tasks"
	"golang.org/x/text/cases"
)

type voiceWorkQuestion struct{ job, input, title, prompt string }

func (q voiceWorkQuestion) wasRead(caption string) bool {
	if q.input == "" || q.prompt == "" {
		return false
	}
	prompt := strings.Join(addressWords(q.prompt), " ")
	words := " " + strings.Join(addressWords(caption), " ") + " "
	return prompt != "" && strings.Contains(words, " "+prompt+" ")
}

type voiceWorkAnswer struct {
	voiceWorkQuestion
	text string
}

var voiceWorkInquiry = regexp.MustCompile(`(?i)^(?:(?:please|snälla|well|okay|ok|wait)[ ,]+)*(?:what|which|who|where|when|why|how|is|are|was|were|do|does|did|can|could|should|would|will|any update|status|tell me|give me an update|vad|vilken|vilket|vem|varför|hur|är|kan|finns det)(?:$|[^\pL])`)
var voiceWorkAnswerPrefix = regexp.MustCompile(`(?i)^(?:the answer is|my answer is|svaret är)\s+`)
var voiceWorkOrdinaryPrefix = regexp.MustCompile(`(?i)^(?:for (?:now|the time being|the moment|me|us)|för (?:tillfället|min del|vår del)),`)
var voiceWorkTitleFold = cases.Fold()

// Resolve a spoken answer before exposing the write tool. An unaddressed
// answer belongs only to the question the call just finished playing.
func (r *voiceRoom) resolveWorkAnswer(transcript string) (voiceWorkAnswer, string) {
	question := r.callerQuestion
	r.callerQuestion = voiceWorkQuestion{}
	// Consume the playback binding on a real utterance. If a newer question
	// finished playing before a late caption arrived, leave that one intact.
	if r.spokenQuestion == question {
		r.spokenQuestion = voiceWorkQuestion{}
		r.spokenQuestionPending = false
	}
	text := strings.TrimSpace(transcript)
	lower := strings.ToLower(text)
	explicit := strings.HasPrefix(lower, "for ") || strings.HasPrefix(lower, "för ")
	answerPrefix := voiceWorkAnswerPrefix.MatchString(text)
	if text == "" || question.input == "" && !explicit && !answerPrefix || strings.HasPrefix(text, `"`) || strings.HasPrefix(text, "“") || strings.HasPrefix(text, "'") ||
		strings.ContainsRune(text, '?') || voiceWorkInquiry.MatchString(text) || spokenAnswerIsInquiry(text, r.directory) {
		return voiceWorkAnswer{}, ""
	}
	_, target, _ := strings.Cut(text, " ")
	var namedPlayedQuestion bool
	if explicit {
		target = strings.TrimSpace(target)
		titles := voiceWorkTitlePrefixes(target)
		playedAnswer, playedMatch := answerForJobTitle(target, question.title, titles)
		matches, err := r.store.WorkStatesByTitle(r.ctx, r.chat.ID, titles)
		if err != nil {
			return voiceWorkAnswer{}, ""
		}
		var found voiceWorkAnswer
		best := playedMatch
		var pending bool
		ambiguous := false
		for _, job := range matches {
			answer, match := answerForJobTitle(target, job.Title, titles)
			if match.length == 0 {
				continue
			}
			switch {
			case best.length == 0 || match.delimited && !best.delimited:
				found = voiceWorkAnswer{voiceWorkQuestion{job: job.ID, input: job.InputID, title: job.Title}, answer}
				best = match
				pending = job.Status == tasks.NeedsInput && job.InputID != ""
				ambiguous = false
			case match.delimited == best.delimited:
				if best == playedMatch && match == playedMatch {
					continue // The question just read aloud identifies a duplicate title.
				}
				ambiguous = true
			}
		}
		switch {
		case ambiguous:
			return voiceWorkAnswer{}, "More than one job matches the caller’s words. Ask them to answer the intended question in Work."
		case found.job != "":
			if !validVoiceWorkAnswer(found.text) {
				return voiceWorkAnswer{}, ""
			}
			if !pending {
				return voiceWorkAnswer{}, "That job has no pending question. Tell the caller that their answer was not sent."
			}
			return found, ""
		case playedMatch.length != 0:
			if !validVoiceWorkAnswer(playedAnswer) {
				return voiceWorkAnswer{}, ""
			}
			text, namedPlayedQuestion = playedAnswer, true
		case !voiceWorkOrdinaryPrefix.MatchString(text):
			// An unmatched preface could introduce a new request or name a
			// different job. Let the Bud respond without binding an answer.
			return voiceWorkAnswer{}, ""
		}
	}
	states, err := r.store.WorkStates(r.ctx, r.chat.ID, time.Now())
	if err != nil {
		return voiceWorkAnswer{}, ""
	}
	for _, job := range states {
		if job.Status == tasks.NeedsInput && job.ID == question.job && job.InputID == question.input {
			return voiceWorkAnswer{question, text}, ""
		}
	}
	if question.input != "" && (answerPrefix || namedPlayedQuestion) {
		return voiceWorkAnswer{}, "The question the caller heard is no longer pending. Tell them it changed and offer to read the current question."
	}
	if answerPrefix {
		for _, job := range states {
			if job.Status == tasks.NeedsInput {
				return voiceWorkAnswer{}, "Ask which job the caller is answering. They can say 'For' followed by the job title and their answer."
			}
		}
	}
	return voiceWorkAnswer{}, ""
}

func voiceWorkTitlePrefixes(text string) []string {
	const maxTitleBytes = 4 * tasks.MaxWorkTitleBytes
	var titles []string
	for i, ch := range text {
		if i > maxTitleBytes {
			return titles
		}
		if i > 0 && (ch == ',' || ch == ':' || unicode.IsSpace(ch)) {
			title := strings.TrimSpace(text[:i])
			if len(titles) == 0 || titles[len(titles)-1] != title {
				titles = append(titles, title)
			}
		}
	}
	return append(titles, text)
}

type voiceWorkTitleMatch struct {
	length    int
	delimited bool
}

func answerForJobTitle(text, title string, prefixes []string) (string, voiceWorkTitleMatch) {
	if title == "" {
		return "", voiceWorkTitleMatch{}
	}
	folded := voiceWorkTitleFold.String(title)
	for _, prefix := range prefixes {
		if voiceWorkTitleFold.String(prefix) == folded {
			answer := strings.TrimSpace(text[len(prefix):])
			delimited := answer != "" && (answer[0] == ',' || answer[0] == ':')
			if delimited {
				answer = strings.TrimSpace(answer[1:])
			}
			return answer, voiceWorkTitleMatch{len(prefix), delimited}
		}
	}
	return "", voiceWorkTitleMatch{}
}

func validVoiceWorkAnswer(text string) bool {
	return text != "" && !voiceWorkInquiry.MatchString(text)
}

func spokenAnswerIsInquiry(text string, agents []tasks.AgentRef) bool {
	name, rest, ok := strings.Cut(text, ",")
	if !ok || !voiceWorkInquiry.MatchString(strings.TrimSpace(rest)) {
		return false
	}
	id, length := addressedVoiceAgent(name, agents, false)
	return id != "" && length == len(addressWords(name))
}
