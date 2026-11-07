package api

import (
	"context"
	"errors"
	"log"
	"slices"
	"strings"
	"time"

	"buddy/internal/tasks"
)

var letterNames = map[string]string{
	"b": "bee", "c": "cee", "d": "dee", "e": "ee", "g": "gee", "j": "jay",
	"k": "kay", "l": "ell", "m": "em", "n": "en", "p": "pee", "q": "cue",
	"r": "ar", "s": "ess", "t": "tee", "u": "you", "v": "vee", "z": "zee",
}

func spokenFloorRequest(text string) string {
	words := strings.Join(addressWords(text), " ")
	for _, prefix := range []string{
		"can i speak to ", "can i talk to ", "could i speak to ", "could i talk to ",
		"i want to speak to ", "i want to talk to ", "let me speak to ", "let me talk to ",
		"i d like to speak to ", "i d like to talk to ", "can i hear from ",
		"let me hear from ", "let s hear from ", "hand it over to ", "hand over to ", "pass the floor to ", "can you hand over to ",
		"kan jag prata med ", "kan jag tala med ", "jag vill prata med ", "låt mig prata med ",
		"lämna över ordet till ", "lämna över till ", "ge ordet till ", "kan du lämna över ordet till ",
	} {
		if rest, ok := strings.CutPrefix(words, prefix); ok {
			return strings.TrimSuffix(rest, " please")
		}
	}
	return ""
}

// Authorize from the caller's own words, before sending the model any job data.
// A correction without a job name is scoped to a just-created handoff.
func spokenWorkStop(text string, agents []tasks.AgentRef, recentJob string) (bool, string) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, `"`) || strings.HasPrefix(text, "“") {
		return false, ""
	}
	if prefix, rest, ok := strings.Cut(text, ","); ok {
		if id, length := addressedVoiceAgent(prefix, agents, false); id != "" && length == len(addressWords(prefix)) {
			text = rest
		}
	}
	trimLead := func(words []string) []string {
		for len(words) > 0 && slices.Contains([]string{"wait", "please", "hey", "okay", "ok", "vänta", "snälla", "nej"}, words[0]) {
			words = words[1:]
		}
		return words
	}
	words := trimLead(addressWords(text))
	if id, length := addressedVoiceAgent(strings.Join(words, " "), agents, false); id != "" && length < len(words) {
		for _, agent := range agents {
			if agent.ID == id && slices.Equal(addressWords(agent.Name), words[:length]) {
				words = trimLead(words[length:])
				break
			}
		}
	}
	command := strings.Join(words, " ")
	command = strings.TrimSuffix(strings.TrimSuffix(command, " please"), " tack")
	for _, prefix := range []string{"can you ", "could you ", "would you ", "can we ", "i want you to ", "kan du ", "skulle du kunna "} {
		if rest, ok := strings.CutPrefix(command, prefix); ok {
			command = strings.TrimPrefix(rest, "please ")
			break
		}
	}
	if recentJob != "" && slices.Contains([]string{
		"stop it", "cancel it", "cancel that", "don t do that", "do not do that", "never mind",
		"avbryt det", "stoppa det", "gör inte det", "strunta i det",
	}, command) {
		return true, recentJob
	}
	verb, target, ok := strings.Cut(command, " ")
	if !ok || !slices.Contains([]string{"stop", "cancel", "abort", "avbryt", "avbryta", "stoppa"}, verb) {
		return false, ""
	}
	targetWords := strings.Fields(target)
	if len(targetWords) > 1 && !slices.Contains([]string{"the", "this", "that", "my", "your", "our", "det", "den", "mitt", "min"}, targetWords[0]) {
		return false, ""
	}
	for _, word := range targetWords {
		if slices.Contains([]string{"about", "after", "and", "before", "but", "during", "for", "from", "if", "not", "or", "unless", "until", "when",
			"efter", "eller", "från", "före", "inte", "men", "när", "och", "om", "tills"}, word) {
			return false, ""
		}
	}
	for _, noun := range []string{"job", "task", "work", "build", "run", "jobbet", "uppgiften", "arbetet", "körningen"} {
		if target == noun || strings.HasSuffix(target, " "+noun) {
			return true, ""
		}
	}
	return false, ""
}

type voiceAddressMode uint8

const (
	addressByName voiceAddressMode = iota
	addressFloorOnly
	addressHandoff
)

func voiceAddress(text string, agents []tasks.AgentRef, leadID string) (string, voiceAddressMode) {
	target := spokenFloorRequest(text)
	if target == "" {
		id, _ := addressedVoiceAgent(text, agents, false)
		return id, addressByName
	}
	id, consumed := addressedVoiceAgent(target, agents, true)
	words := strings.Fields(target)
	if consumed == 0 {
		for _, name := range [][]string{{"the", "lead", "bud"}, {"the", "lead"}, {"the", "leader"}, {"ledaren"}} {
			if len(words) >= len(name) && slices.Equal(words[:len(name)], name) {
				id, consumed = leadID, len(name)
				break
			}
		}
	}
	if id != "" && consumed == len(words) {
		return id, addressFloorOnly
	}
	return id, addressHandoff
}

func floorReply(member *roomMember) *roomReply {
	return &roomReply{member: member,
		instructions: "The caller asked to speak to you. Acknowledge you're here in one short sentence and wait for their question."}
}

func (r *voiceRoom) queueTeam(title, brief string, agents []string, allowEdits bool) (string, error) {
	if r.teamQueued {
		return "The team is already queued to continue after the call.", nil
	}
	if r.worker == nil {
		return "Buddy's background worker is unavailable.", nil
	}
	if len(agents) == 0 {
		for _, member := range r.members {
			agents = append(agents, member.agent.ID)
		}
	} else {
		for _, id := range agents {
			if r.member(id) == nil {
				return "Choose agents who are in this call.", nil
			}
		}
	}
	err := r.store.QueueVoiceTeam(r.ctx, r.chat.ID, r.id, title, brief, agents, allowEdits)
	switch {
	case errors.Is(err, tasks.ErrQueueFull):
		return "Buddy's work queue is full. Try again after some tasks finish.", nil
	case err != nil:
		return voiceActionError(err, "Couldn't queue the team to continue in chat."), nil
	}
	r.teamQueued = true
	message := "The team will continue and post updates in this group chat when the call ends."
	return message, r.send(VoiceEvent{Type: "team_queued", Text: message})
}

func (r *voiceRoom) releaseTeam() {
	if !r.teamQueued {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.ctx), 5*time.Second)
	defer cancel()
	if err := r.store.ReleaseVoiceTeam(ctx, r.id); err != nil {
		log.Printf("release call team: %v", err)
		return
	}
	r.worker.WakeBackground()
}
