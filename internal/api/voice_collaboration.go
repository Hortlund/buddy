package api

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"buddy/internal/tasks"
)

func (r *voiceRoom) member(id string) *roomMember {
	for _, member := range r.members {
		if member.agent.ID == id {
			return member
		}
	}
	return nil
}

// An invitation only becomes a group membership after its voice session connects.
func (r *voiceRoom) invite(id string, invitation *roomInvitation) (string, error) {
	switch {
	case r.member(id) != nil:
		return "That agent is already in the call.", nil
	case r.joining:
		return "Another agent is already joining the call.", nil
	case len(r.members) >= len(callVoices):
		return "This call already has eight agents.", nil
	}
	agent, err := r.store.Agent(r.ctx, id)
	if err != nil || agent.Removed {
		return "That agent isn't available.", nil
	}
	index := 0
	for slices.ContainsFunc(r.members, func(m *roomMember) bool { return m.voiceIndex == index }) {
		index++
	}
	r.invitation = invitation
	r.join(agent, index)
	return "", r.send(VoiceEvent{Type: "joining", Agent: agent.ID, Speaker: agent.Name})
}

func (r *voiceRoom) finishJoin(joined joinResult) error {
	invitation := r.invitation
	r.joining, r.invitation = false, nil
	member, err := joined.member, joined.err
	message := ""
	if err != nil {
		message = err.Error()
	} else if err = r.addMember(member); err != nil {
		if r.member(member.agent.ID) != nil {
			return err // Membership was saved, but the client connection failed.
		}
		message = voiceActionError(err, "Couldn't add "+member.agent.Name+" to the call.")
	}
	if message != "" {
		if member.conn != nil {
			_ = member.conn.CloseNow()
		}
		if r.selected == nil {
			return r.send(VoiceEvent{Type: "error", Text: message})
		}
		if err := r.notice(message); err != nil {
			return err
		}
	}
	if message == "" && invitation == nil && len(r.members) > 1 {
		r.greetings = append(r.greetings, member)
		return r.advance()
	}
	if invitation == nil || r.member(invitation.source.agent.ID) != invitation.source {
		return nil
	}
	if invitation.callID != "" {
		output := message
		if output == "" {
			output = member.agent.Name + " has joined the call and group chat and will speak next."
		}
		if err := r.toolOutput(invitation.source, invitation.callID, output); err != nil {
			return err
		}
	}
	if r.turn != invitation.turn {
		if message == "" {
			r.greetings = append(r.greetings, member)
		}
		return r.advance()
	}
	switch {
	case message != "":
		r.reply = &roomReply{member: invitation.source, instructions: "Briefly tell the caller this invitation failed: " + message}
	case invitation.addressed:
		r.reply = addressedReply(member, invitation.addressMode)
		r.reply.instructions = "Say a brief hello because you just joined. " + r.reply.instructions
	case invitation.handoff:
		r.floorHandoff = handoffReply(member, invitation.source, invitation.question)
		r.floorHandoff.instructions = "Say a brief hello because you just joined. " + r.floorHandoff.instructions
	default:
		r.greetings = append(r.greetings, member)
	}
	return r.advance()
}

func namedReply(member *roomMember) *roomReply {
	return &roomReply{member: member, tools: voiceCaller,
		instructions: "The caller addressed you by name. Respond to their latest utterance in the shared transcript."}
}

func addressedReply(member *roomMember, mode voiceAddressMode) *roomReply {
	if mode == addressFloorOnly {
		return floorReply(member)
	}
	reply := namedReply(member)
	if mode == addressHandoff {
		reply.instructions = "The caller has already given you the floor. Address the question or task they included in that handoff."
	}
	return reply
}

func teammateReply(member, source *roomMember, question string) *roomReply {
	return &roomReply{member: member, tools: voiceTeammate,
		instructions: "You have the floor. " + source.agent.Name + " asked for your perspective in the call. " +
			"Answer their question aloud using the shared transcript. Their question is: " + question}
}

func handoffReply(member, source *roomMember, question string) *roomReply {
	if question != "" {
		return teammateReply(member, source, question)
	}
	return &roomReply{member: member, instructions: "You have the floor. Briefly acknowledge the handoff and address any open question in the shared transcript. If there is no question, let the caller know you are here."}
}

func (r *voiceRoom) leadInstructions() string {
	if lead := r.member(r.leadID); lead != nil {
		return "The Lead Bud in this call is " + lead.agent.Name + " (ID: " + lead.agent.ID + "). " +
			"The lead answers open questions. A Bud addressed by name answers directly. Anyone may speak when given the floor."
	}
	for _, bud := range r.directory {
		if bud.ID == r.leadID {
			return "The Lead Bud for this chat is " + bud.Name + " (ID: " + bud.ID + "), who has not joined the call yet."
		}
	}
	return ""
}

func (r *voiceRoom) routeUser(source *roomMember, transcript string) error {
	r.callerWords = transcript
	if r.chat.Name != "" {
		if lead, err := r.store.VoiceLeadID(r.ctx, r.chat.ID); err == nil && lead != r.leadID {
			r.leadID = lead
			if err := r.roster("roster"); err != nil {
				return err
			}
		}
	}
	r.reply = &roomReply{member: source}
	if strings.TrimSpace(transcript) == "" {
		return r.advance()
	}
	var recentJobs []string
	if r.turn == r.handoffTurn+1 {
		recentJobs = r.handoffJobs
	}
	var ambiguousStop bool
	r.stopRequested, r.stopTarget, ambiguousStop = spokenWorkStop(transcript, r.directory, recentJobs)
	if r.stopRequested || ambiguousStop {
		r.callerQuestion, r.workAnswer = voiceWorkQuestion{}, voiceWorkAnswer{}
	} else {
		var clarification string
		r.workAnswer, clarification = r.resolveWorkAnswer(transcript)
		if clarification != "" {
			r.reply.instructions = clarification
			return r.advance()
		}
	}
	if ambiguousStop {
		r.reply.instructions = "Several jobs were handed off in the last turn. Ask the caller to name the job in their stop request, for example 'stop the release review job'."
		return r.advance()
	}
	r.reply.tools = voiceCaller
	id, mode := voiceAddress(transcript, r.directory, r.leadID)
	manual := r.manualFocus
	r.manualFocus = nil
	if id == "" {
		if manual != nil {
			r.reply = namedReply(manual)
			r.reply.instructions = "The caller selected you to answer their latest utterance."
			return r.advance()
		}
		if lead := r.member(r.leadID); lead != nil && lead != source {
			r.reply = &roomReply{member: lead, tools: voiceCaller,
				instructions: "The caller left this question open to the Lead Bud. Answer their latest utterance from the shared transcript."}
		}
		return r.advance()
	}
	if id == source.agent.ID {
		if mode != addressByName {
			r.reply = addressedReply(source, mode)
		}
		return r.advance()
	}
	if member := r.member(id); member != nil {
		r.reply = addressedReply(member, mode)
		return r.advance()
	}
	invitation := &roomInvitation{source: source, turn: r.turn, addressed: true, addressMode: mode}
	message, err := r.invite(id, invitation)
	if err != nil {
		return err
	}
	if message == "" {
		r.reply = nil // The invited agent answers after joining.
		return nil
	}
	r.reply.instructions = "The caller tried to address another agent. Tell them: " + message
	r.reply.tools = voiceSpeakOnly
	return r.advance()
}

func addressWords(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// Match a spoken address at the start, or a vocative at the end after a comma.
// Merely mentioning an agent inside a sentence doesn't take the floor away.
func addressedVoiceAgent(transcript string, agents []tasks.AgentRef, floorTarget bool) (id string, length int) {
	words := addressWords(transcript)
	if len(words) == 0 {
		return "", 0
	}
	greeted := false
	for len(words) > 1 && slices.Contains([]string{"hey", "hi", "hello", "hej", "okay", "ok", "yo"}, words[0]) {
		words = words[1:]
		greeted = true
	}
	var leading, trailing []string
	if i := strings.IndexAny(transcript, ".,:!?"); i >= 0 {
		leading = addressWords(transcript[:i])
	}
	if _, tail, ok := strings.Cut(strings.TrimSpace(transcript), ","); ok {
		if i := strings.LastIndex(tail, ","); i >= 0 {
			tail = tail[i+1:]
		}
		trailing = addressWords(tail)
	}
	bestExact := false
	for _, agent := range agents {
		name := addressWords(agent.Name)
		if len(name) == 0 {
			continue
		}
		starts := len(words) >= len(name) && slices.Equal(words[:len(name)], name)
		ends := slices.Equal(trailing, name)
		approximate := false
		if len(name) == 1 && len([]rune(name[0])) == 1 {
			addressed := floorTarget || greeted || len(words) == 1 || len(leading) == 1 && leading[0] == words[0]
			starts = starts && addressed
			approximate = addressed && spokenLetter(name[0], words[0]) || len(trailing) == 1 && spokenLetter(name[0], trailing[0])
		} else {
			approximate = len(words) >= len(name) && stretchedName(name, words[:len(name)]) || stretchedName(name, trailing)
		}
		exact := starts || ends
		if !exact && !approximate || bestExact && !exact {
			continue
		}
		if exact && !bestExact || len(name) > length {
			id, length, bestExact = agent.ID, len(name), exact
		} else if len(name) == length {
			id = "" // Duplicate names need an explicit choice.
		}
	}
	return id, length
}

func spokenLetter(letter, word string) bool {
	alias := letterNames[letter]
	return alias != "" && (word == alias || alias == letter+"ee" && word == letter+"e")
}

// Captions may shorten a name with an exaggerated final letter. Apply this
// only to runs of three or more so ordinary doubled letters stay significant.
func stretchedName(name, words []string) bool {
	if len(words) != len(name) || !slices.Equal(name[:len(name)-1], words[:len(words)-1]) {
		return false
	}
	shorten := func(word string) string {
		letters := []rune(word)
		i := len(letters) - 1
		for i > 0 && letters[i-1] == letters[len(letters)-1] {
			i--
		}
		if len(letters)-i >= 3 {
			return string(letters[:i+1])
		}
		return word
	}
	return shorten(name[len(name)-1]) == shorten(words[len(words)-1])
}

func (r *voiceRoom) toolOutput(member *roomMember, callID, output string) error {
	return rtWrite(r.ctx, member.conn, object{"type": "conversation.item.create", "event_id": "buddy_tool_" + rand.Text(), "item": object{
		"type": "function_call_output", "call_id": callID, "output": output,
	}})
}

func (r *voiceRoom) runTools(member *roomMember, event realtimeEvent) error {
	if r.tools == voiceSpeakOnly || event.Response.Status != "completed" {
		return nil
	}
	used := make(map[string]bool)
	hadBudResult := r.didCreateBud
	for _, item := range event.Response.Output {
		if item.Type != "function_call" || item.CallID == "" {
			continue
		}
		if r.toolSteps <= 0 || used[item.Name] {
			if err := r.toolOutput(member, item.CallID, "This tool has already been used in this response or the turn reached its tool limit."); err != nil {
				return err
			}
			continue
		}
		used[item.Name] = true
		r.toolSteps--
		output, deferred, err := r.performTool(member, item)
		if err != nil {
			return err
		}
		if deferred {
			continue
		}
		if err := r.toolOutput(member, item.CallID, output); err != nil {
			r.logf("tool output relay failed bud=%s error_type=%T", member.agent.ID, err)
			return r.loseMember(member)
		}
		if r.reply == nil {
			if item.Name == "ask_agent" && r.floorHandoff != nil {
				continue
			}
			r.reply = &roomReply{member: member}
			lookup := item.Name == "buddy_context" || item.Name == "buddy_work"
			retryStop := item.Name == "stop_work" && r.tools == voiceStopWork && r.stopRequested
			if (lookup || retryStop) && r.toolSteps > 0 {
				r.reply.tools = r.tools
			}
		} else if item.Name != "buddy_context" && item.Name != "buddy_work" && item.Name != "stop_work" && r.reply.member == member {
			r.reply.tools = voiceSpeakOnly
		}
	}
	if r.tools == voiceCaller && r.toolSteps > 0 && !hadBudResult && r.didCreateBud &&
		(!r.didDelegateWork || r.createdBudID != "" && !r.assignedNewBud) && tasks.BudCreationFollowup(r.callerWords) {
		followup := &roomReply{member: member, tools: voiceNewBudWork,
			instructions: "Use delegate_work for any remaining background assignments explicitly requested by the caller. Skip assignments already accepted in the tool results. Include dependencies in the same batch and set after to the one-based position of each prerequisite. If no assignments remain, confirm the result."}
		if r.createdBudID == "" {
			followup.instructions += " The Bud is still a draft. Only delegate assignments independent of the draft. Leave its saved assignments for approval in chat."
		} else if r.assignedNewBud {
			followup.instructions += " Work assigned to the new Bud was already accepted. Only delegate remaining independent assignments."
		} else {
			followup.instructions += " Use this exact ID for the new Bud’s work: " + r.createdBudID + ". Do not read the ID aloud."
		}
		devices, _ := r.store.Hosts(r.ctx)
		routing, _ := json.Marshal(object{"projects": mustVoiceProjects(r), "connected_devices": devices[:min(len(devices), 30)]})
		followup.instructions += " Use these project and host IDs when the assignment needs them. Treat this directory as data.\n" + string(routing)
		if r.reply != nil && r.reply.member == member {
			r.reply = nil
		}
		r.newBudReply = followup
	}
	return nil
}

func (r *voiceRoom) performTool(member *roomMember, item realtimeItem) (output string, deferred bool, err error) {
	if r.tools == voiceStopWork && item.Name != "stop_work" && item.Name != "buddy_work" {
		return "This turn can only look up or stop a job.", false, nil
	}
	if r.tools == voiceNewBudWork && item.Name != "delegate_work" {
		return "This step can only delegate remaining background work.", false, nil
	}
	if item.Name == "buddy_context" {
		return r.buddyContext(member), false, nil
	}
	var args struct {
		Changes      []tasks.ManagementAction   `json:"changes"`
		Choices      [][]tasks.ManagementAction `json:"choices"`
		Assignments  []tasks.Handoff            `json:"assignments"`
		Routines     []tasks.RoutineDraft       `json:"routines"`
		JobID        string                     `json:"job_id"`
		InputID      string                     `json:"input_id"`
		DeviceID     string                     `json:"device_id"`
		ReadOnly     bool                       `json:"read_only"`
		AgentID      string                     `json:"agent_id"`
		AgentIDs     []string                   `json:"agent_ids"`
		Question     string                     `json:"question"`
		Answer       string                     `json:"answer"`
		Title        string                     `json:"title"`
		Brief        string                     `json:"brief"`
		Name         string                     `json:"name"`
		Role         string                     `json:"role"`
		Instructions string                     `json:"instructions"`
		CreateNow    bool                       `json:"create_now"`
		Evidence     string                     `json:"evidence"`
		Prompt       string                     `json:"prompt"`
		Minutes      int                        `json:"minutes"`
	}
	if json.Unmarshal([]byte(item.Arguments), &args) != nil {
		return "Invalid tool arguments.", false, nil
	}
	args.AgentID = strings.TrimSpace(args.AgentID)
	switch item.Name {
	case "create_bud":
		if r.tools != voiceCaller || r.didCreateBud || strings.TrimSpace(r.callerWords) == "" {
			return "Bud creation requires a new request from the caller.", false, nil
		}
		if len(args.Assignments) != 0 && (!tasks.BudDraftRequest(r.callerWords, member.agent.Name) || !tasks.BudCreationFollowup(r.callerWords)) {
			return "The latest request does not include an explicit assignment for the new Bud. Save the profile without assignments.", false, nil
		}
		draft := tasks.BudDraft{Name: args.Name, Role: args.Role, Instructions: args.Instructions,
			CreateNow: args.CreateNow, Evidence: args.Evidence}
		bud, jobs, notice, err := r.store.SaveVoiceBud(r.ctx, r.chat.ID, r.id, member.agent.ID, r.callerWords, draft, args.Assignments...)
		if err != nil {
			return voiceActionError(err, "Couldn’t save the Bud."), false, nil
		}
		r.didCreateBud = true
		if bud.ID == "" {
			if notice == "" {
				notice = "The Bud draft is in this call’s chat. The caller can create it there."
			}
			return notice, false, r.send(VoiceEvent{Type: "bud_saved", Text: "Bud draft added to chat"})
		}
		r.createdBudID = bud.ID
		if err := r.send(VoiceEvent{Type: "bud_saved", Item: bud.ID, Text: bud.Name + " was created"}); err != nil {
			return "", false, err
		}
		return strings.TrimSpace(bud.Name + " was created. Its Bud ID is " + bud.ID + ". Its profile and chat are available from this call’s chat. " + notice), false, r.recordVoiceHandoffs(jobs)
	case "buddy_work":
		output, question := r.buddyWork(member, strings.TrimSpace(args.JobID))
		if question.input != "" && (r.reply == nil || r.reply.member == member) {
			if r.reply == nil {
				r.reply = &roomReply{member: member, tools: r.tools}
			}
			r.reply.question = question
			r.reply.instructions += "\nIf you ask the caller to answer the job's pending question, read it verbatim so their next reply reaches that question."
		}
		return output, false, nil
	case "answer_work":
		answer := strings.TrimSpace(args.Answer)
		bound := r.workAnswer
		if r.tools != voiceCaller || r.worker == nil || bound.input == "" || args.JobID != bound.job || args.InputID != bound.input || answer != bound.text {
			return "There is no matching spoken answer for that question. Ask which job the caller is answering, or have them reply in Work.", false, nil
		}
		resumed, err := r.store.AnswerWorkInput(r.ctx, r.chat.ID, bound.input, answer, "voice:"+r.id+":"+item.CallID, nil)
		if err != nil {
			return voiceActionError(err, "Couldn’t resume that job. Check its current status."), false, nil
		}
		if resumed {
			r.worker.WakeBackground()
		}
		r.workAnswer = voiceWorkAnswer{}
		return "The answer was saved. The job will continue and post updates in this chat.", false, r.send(VoiceEvent{Type: "work_changed"})
	case "stop_work":
		return r.stopWork(strings.TrimSpace(args.JobID)), false, nil
	case "manage_buddy":
		if r.tools != voiceCaller || r.worker == nil || r.didManage || r.callerWords == "" {
			return "Buddy changes require a new request from the caller.", false, nil
		}
		for _, plan := range append(slices.Clone(args.Choices), args.Changes) {
			for _, change := range plan {
				if !slices.Contains([]string{"stop_job", "pause_routine", "resume_routine", "delete_routine", "pause_watch", "resume_watch", "archive_watch"}, change.Operation) {
					return "That Buddy change is available in text chat.", false, nil
				}
			}
		}
		note, err := r.worker.ManageVoice(r.ctx, r.chat.ID, member.agent.ID, r.id, r.callerWords, args.Changes, args.Choices)
		if err != nil {
			return voiceActionError(err, "I couldn’t apply those changes."), false, nil
		}
		r.didManage = true
		return note, false, r.send(VoiceEvent{Type: "work_changed"})
	case "invite_agent", "ask_agent":
		if r.hops <= 0 {
			return "The call is ready for the caller's next turn.", false, nil
		}
		if args.AgentID == "" || args.AgentID == member.agent.ID {
			return "Choose another agent's ID from buddy_context.", false, nil
		}
		question := ""
		if item.Name == "ask_agent" {
			question = clipVoice(strings.TrimSpace(args.Question), 2000)
		}
		r.hops--
		if target := r.member(args.AgentID); target != nil && item.Name == "ask_agent" {
			r.floorHandoff = handoffReply(target, member, question)
			r.logf("handoff from=%s to=%s", member.agent.ID, target.agent.ID)
			return target.agent.Name + " has the next speaking turn.", false, nil
		}
		message, err := r.invite(args.AgentID, &roomInvitation{
			source: member, callID: item.CallID, turn: r.turn, question: question, handoff: item.Name == "ask_agent",
		})
		return message, message == "" && err == nil, err
	case "continue_in_chat":
		if r.tools != voiceCaller {
			return "Only the caller can request post-call work.", false, nil
		}
		message, err := r.queueTeam(args.Title, args.Brief, args.AgentIDs, true)
		return message, false, err
	case "delegate_work":
		if (r.tools != voiceCaller && r.tools != voiceNewBudWork) || strings.TrimSpace(r.callerWords) == "" {
			return "Only the caller can request this action.", false, nil
		}
		if r.worker == nil {
			return "Buddy's background worker is unavailable.", false, nil
		}
		assignments := args.Assignments
		if assignments == nil {
			// Older voice sessions may still send the single-assignment shape.
			assignments = []tasks.Handoff{{AgentID: args.AgentID, DeviceID: args.DeviceID,
				Title: args.Title, Brief: args.Brief, ReadOnly: args.ReadOnly}}
		}
		if r.tools == voiceNewBudWork && r.assignedNewBud && slices.ContainsFunc(assignments, func(assignment tasks.Handoff) bool {
			return strings.TrimSpace(assignment.AgentID) == r.createdBudID
		}) {
			return "The new Bud’s work was already accepted. Only delegate remaining independent assignments.", false, nil
		}
		jobs, err := r.store.QueueVoiceAssignments(r.ctx, r.chat.ID, member.agent.ID, r.id, member.agent.ID+":"+item.CallID, assignments)
		if err != nil {
			return voiceActionError(err, "Couldn't queue the background assignments."), false, nil
		}
		r.didDelegateWork = true
		assigned := make([]object, 0, len(jobs))
		for _, job := range jobs {
			assigned = append(assigned, object{"id": job.ID, "bud": job.AgentName, "title": job.Title, "status": job.Status})
		}
		data, _ := json.Marshal(assigned)
		return "The handoff was accepted. Briefly say who will do what. Results and questions will appear in chat, and the caller can keep talking. Assignments: " + string(data),
			false, r.recordVoiceHandoffs(jobs)
	case "schedule_routine":
		if r.tools != voiceCaller || strings.TrimSpace(r.callerWords) == "" {
			return "Only the caller can request this action.", false, nil
		}
		if r.didSchedule {
			return "Routines have already been scheduled for this spoken request.", false, nil
		}
		drafts := args.Routines
		if drafts == nil {
			drafts = []tasks.RoutineDraft{{AgentID: args.AgentID, DeviceID: args.DeviceID,
				Name: args.Name, Prompt: args.Prompt, Minutes: args.Minutes}}
		}
		routines := make([]tasks.Routine, 0, len(drafts))
		for _, draft := range drafts {
			id := strings.TrimSpace(draft.AgentID)
			if id == "" {
				id = member.agent.ID
			}
			routines = append(routines, tasks.Routine{AgentID: id, DeviceID: strings.TrimSpace(draft.DeviceID),
				Name: draft.Name, Prompt: draft.Prompt, Minutes: draft.Minutes})
		}
		saved, err := r.store.AddVoiceRoutines(r.ctx, r.chat.ID, r.id, routines)
		if err != nil {
			return voiceActionError(err, "Couldn’t schedule the routines."), false, nil
		}
		r.didSchedule = true
		receipts := make([]string, 0, len(saved))
		for _, routine := range saved {
			receipts = append(receipts, fmt.Sprintf("%s (%s)", routine.Name, tasks.RoutineCadence(routine.Minutes)))
		}
		return "Scheduled " + strings.Join(receipts, ", ") + ".", false, r.send(VoiceEvent{Type: "work_changed"})
	default:
		return "This tool isn't available in a call.", false, nil
	}
}

func (r *voiceRoom) recordVoiceHandoffs(jobs []tasks.AssignedWork) error {
	if len(jobs) == 0 {
		return nil
	}
	if r.handoffTurn != r.turn {
		r.handoffJobs, r.handoffTurn = r.handoffJobs[:0], r.turn
	}
	for _, job := range jobs {
		if !slices.Contains(r.handoffJobs, job.ID) {
			r.handoffJobs = append(r.handoffJobs, job.ID)
		}
		r.assignedNewBud = r.assignedNewBud || job.AgentID == r.createdBudID
	}
	if r.worker != nil {
		r.worker.WakeBackground()
	}
	return r.send(VoiceEvent{Type: "work_queued", Item: jobs[0].ID, Text: jobs[0].Title})
}

func (r *voiceRoom) stopWork(id string) string {
	if r.tools != voiceStopWork || !r.stopRequested {
		return "The caller didn't authorize a job stop in this turn."
	}
	if id == "" {
		return "Use buddy_work to find the job's ID."
	}
	if r.stopTarget != "" && id != r.stopTarget {
		return "Use the just-started job's ID: " + r.stopTarget
	}
	if r.worker == nil {
		r.stopRequested = false
		return "Buddy's background worker is unavailable."
	}
	job, err := r.store.WorkDetails(r.ctx, r.chat.ID, id)
	if err != nil {
		return "That job isn't available in this chat."
	}
	r.stopRequested = false
	stopped, err := r.worker.Cancel(r.ctx, job.ID, false)
	if errors.Is(err, tasks.ErrInactive) {
		current, err := r.store.WorkDetails(r.ctx, r.chat.ID, job.ID)
		if err != nil {
			return "The job is no longer available."
		}
		if current.Status == tasks.Cancelled {
			return "The job was already stopped."
		}
		if current.Status == tasks.Running {
			return "The job is already finishing and could not be stopped."
		}
		return fmt.Sprintf("The job is no longer running. Its status is %s.", current.Status)
	}
	if err != nil {
		return "Couldn't stop the job. Check its status before trying again."
	}
	r.logf("stop work job=%s status=%s", stopped.ID, stopped.Status)
	if stopped.Status == tasks.Cancelling {
		return "The stop request was accepted. The job is stopping."
	}
	return "The job was stopped."
}

func voiceActionError(err error, fallback string) string {
	var input *tasks.InputError
	if errors.As(err, &input) {
		return input.Error()
	}
	return fallback
}

func (r *voiceRoom) buddyContext(member *roomMember) string {
	if err := r.refreshTeam(); err != nil {
		return "Couldn't load the current Buddy team."
	}
	directory, err := r.store.Directory(r.ctx)
	if err == nil {
		r.directory, r.primaryID = directory.Agents, directory.PrimaryAgentID
	}
	chat, err := r.store.Conversation(r.ctx, r.chat.ID, 0)
	if err != nil {
		return "Couldn't load Buddy's current chat."
	}
	work := make([]object, 0, min(len(chat.Work), 12))
	for _, item := range chat.Work[:min(len(chat.Work), 12)] {
		work = append(work, object{"id": item.ID, "title": item.Title, "agent": item.AgentName, "status": item.Status})
	}
	routines := make([]object, 0, len(chat.Routines))
	for _, routine := range chat.Routines {
		routines = append(routines, object{"id": routine.ID, "name": routine.Name, "agent_id": routine.AgentID,
			"minutes": routine.Minutes, "enabled": routine.Enabled})
	}
	var files []string
	if workspace, err := r.store.Workspace(r.ctx, member.agent.ID); err == nil {
		for _, entry := range workspace.Entries {
			files = append(files, clipVoice(entry.Name, 150))
		}
	}
	var watches []object
	if items, err := r.store.Watches(r.ctx); err == nil {
		for _, watch := range items {
			if watch.ConversationID == r.chat.ID {
				watches = append(watches, object{"id": watch.ID, "name": watch.Name, "enabled": watch.Enabled})
			}
		}
	}
	agents := make([]object, 0, min(len(r.directory), 80))
	for _, agent := range r.directory[:min(len(r.directory), 80)] {
		agents = append(agents, object{"id": agent.ID, "name": agent.Name, "role": agent.Role, "in_call": r.member(agent.ID) != nil,
			"primary": agent.ID == r.primaryID, "call_lead": agent.ID == r.leadID})
	}
	title := chat.Name
	if title == "" {
		title = chat.Agent.Name
	}
	devices, _ := r.store.Hosts(r.ctx)
	data, _ := json.Marshal(object{"chat": title, "agents": agents, "primary_bud_id": r.primaryID, "lead_bud_id": r.leadID, "buddy_team": r.team,
		"projects": mustVoiceProjects(r), "your_workspace_entries": files, "work": work, "routines": routines, "watches": watches, "connected_devices": devices[:min(len(devices), 30)]})
	return string(data)
}

func (r *voiceRoom) buddyWork(member *roomMember, id string) (string, voiceWorkQuestion) {
	stopLookup := r.tools == voiceStopWork
	if id != "" && !stopLookup {
		detail, err := r.store.WorkDetails(r.ctx, r.chat.ID, id)
		if err == nil {
			detail.Brief, detail.Result = clipVoice(detail.Brief, 2400), clipVoice(detail.Result, 3500)
			detail.Question = clipVoice(detail.Question, 2400)
			detail.Events = detail.Events[max(0, len(detail.Events)-5):]
			detail.Steering = nil
			data, _ := json.Marshal(detail)
			var question voiceWorkQuestion
			if detail.Status == tasks.NeedsInput && detail.InputID != "" {
				question = voiceWorkQuestion{detail.ID, detail.InputID, detail.Title, detail.Question}
			}
			return string(data), question
		}
	}
	var items []tasks.WorkItem
	var err error
	if stopLookup {
		items, err = r.store.WorkOverview(r.ctx, r.chat.ID)
	} else {
		items, err = r.store.WorkContext(r.ctx, r.chat.ID, member.agent.ID)
	}
	if err != nil {
		return "Couldn't read current job status.", voiceWorkQuestion{}
	}
	list := make([]object, 0, len(items))
	for _, item := range items {
		if id != "" && !stopLookup && item.ID != id {
			continue
		}
		entry := object{"id": item.ID, "title": item.Title, "bud": item.AgentName, "status": item.Status}
		if !stopLookup {
			entry["in_this_chat"] = item.ConversationID == r.chat.ID
			if item.ConversationID == r.chat.ID {
				entry["activity"], entry["latest_update"] = clipVoice(item.Activity, 300), clipVoice(item.Update, 500)
			}
		}
		list = append(list, entry)
	}
	if id != "" && !stopLookup && len(list) == 0 {
		team, err := r.store.TeamContext(r.ctx, r.chat.ID, member.agent.ID)
		if err == nil {
			for _, job := range team.Assignments {
				if job.ID == id {
					data, _ := json.Marshal(job)
					return string(data), voiceWorkQuestion{}
				}
			}
		}
		return "That job isn't available to you.", voiceWorkQuestion{}
	}
	data, _ := json.Marshal(list)
	return string(data), voiceWorkQuestion{}
}

func mustVoiceProjects(r *voiceRoom) []tasks.Project {
	projects, _ := r.store.Projects(r.ctx)
	return projects
}
