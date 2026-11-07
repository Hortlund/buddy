package main

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"buddy/internal/api"
	"buddy/internal/tasks"
	"buddy/internal/voiceaudio"
)

type desktopVoice struct {
	work         workBrowser
	workOpen     bool
	cancel       context.CancelFunc
	commands     chan api.VoiceEvent
	muted        atomic.Bool
	chat         string
	title        string
	ready        bool
	ending       bool
	joining      string
	joiningID    string
	focus        string
	lead         string
	next         string
	speaker      string
	caption      string
	notice       string
	members      []api.VoiceMember
	profiles     map[string]tasks.Agent
	loading      map[string]bool
	started      time.Time
	speaking     string
	waiting      string
	hearing      bool
	routing      bool
	teamQueued   bool
	teamPending  bool
	micLevel     float32
	expanded     bool
	openingAudio bool
	micReady     bool
	micStatus    string
	directMic    bool
}

func (a *desktop) startVoice() {
	if a.voice != nil || a.window == nil || !a.online || a.chat.ID != a.currentConversation() || a.chat.Agent.Removed || !voiceaudio.Available() {
		return
	}
	if a.serverProtocol < api.VoiceProtocol {
		a.voiceErr = "Update your Buddy server to use live calls."
		return
	}
	agent := a.focusedAgent()
	if a.chat.Name != "" {
		agent = a.chat.Agent
	}
	title := a.chat.Name
	if title == "" {
		title = agent.Name
	}
	ctx, cancel := context.WithCancel(a.ctx)
	call := &desktopVoice{
		cancel: cancel, commands: make(chan api.VoiceEvent, 8), chat: a.chat.ID, title: title,
		members: []api.VoiceMember{{ID: agent.ID, Name: agent.Name}}, focus: agent.ID, lead: agent.ID, expanded: true,
		directMic: a.directVoiceMic,
		profiles:  map[string]tasks.Agent{agent.ID: agent}, loading: make(map[string]bool),
	}
	call.profiles[a.chat.Agent.ID] = a.chat.Agent
	for _, profile := range a.chat.Profiles {
		call.profiles[profile.ID] = profile
	}
	if a.micCheckCancel != nil {
		a.micCheckCancel()
	}
	a.voice, a.voiceErr = call, ""
	direct, initialChat := call.directMic, call.chat
	client, win := a.client, a.window
	go func() {
		defer cancel()
		err := runDesktopVoice(ctx, client, call, initialChat, agent.ID, direct, func(event api.VoiceEvent) {
			win.Update(func() {
				if a.voice != call || call.ending && event.Type != "roster" {
					return
				}
				switch event.Type {
				case "ready", "roster":
					if event.Chat != "" && event.Chat != call.chat {
						previous := call.chat
						call.chat = event.Chat
						if a.section == "" && a.currentConversation() == previous {
							a.openConversation(call.chat)
						}
					}
					if event.Title != "" {
						call.title = event.Title
					}
					call.ready, call.members, call.focus, call.next = true, event.Participants, event.Agent, event.Next
					if event.Lead != "" {
						call.lead = event.Lead
					}
					if call.started.IsZero() {
						call.started = time.Now()
					}
					if event.Type == "roster" && call.joining != "" && call.contains(call.joiningID) {
						call.joining, call.joiningID = "", ""
					}
					a.refreshSoon()
					for _, member := range call.members {
						id := member.ID
						if _, ok := call.profiles[id]; ok || call.loading[id] {
							continue
						}
						call.loading[id] = true
						go func() {
							profile, err := client.Agent(ctx, id)
							win.Update(func() {
								if a.voice != call {
									return
								}
								delete(call.loading, id)
								if err == nil {
									call.profiles[id] = profile
								}
							})
						}()
					}
				case "opening_audio":
					call.openingAudio = true
					call.micReady, call.micLevel, call.micStatus = false, 0, event.Text
				case "mic_ready":
					call.micReady, call.micStatus = true, ""
				case "joining":
					call.joining, call.joiningID = event.Speaker, event.Agent
				case "audio_start":
					call.routing = false
					call.speaking, call.waiting, call.hearing = event.Agent, "", false
				case "listening":
					call.routing = false
					call.speaking, call.waiting, call.hearing = "", "", true
					call.speaker, call.caption = "", ""
				case "routing":
					call.routing, call.hearing = true, false
				case "responding":
					call.routing = false
					call.waiting, call.hearing = event.Agent, false
					call.speaker, call.caption = "", ""
				case "interrupt", "settled":
					call.speaking, call.waiting = "", ""
				case "idle":
					call.hearing, call.routing = false, false
				case "work_queued":
					call.workOpen = true
					call.work.nextPoll = time.Time{}
					a.refreshSoon()
				case "work_changed":
					call.work.nextPoll = time.Time{}
					a.refreshSoon()
				case "team_queued":
					call.teamQueued, call.teamPending = true, false
					call.notice = event.Text
					a.refreshSoon()
				case "mic_level":
					call.micLevel = event.Level
				case "direct_mic":
					call.directMic, a.directVoiceMic = true, true
				case "duplex_mic":
					call.directMic, a.directVoiceMic = false, false
				case "caption":
					call.speaker, call.caption = event.Speaker, event.Text
				case "final":
					if event.Speaker != "You" || call.waiting == "" && call.speaking == "" && !call.hearing {
						call.speaker, call.caption = event.Speaker, event.Text
					}
					a.refreshSoon()
				case "notice":
					call.teamPending = false
					call.notice, call.joining, call.joiningID = event.Text, "", ""
				}
			})
		})
		win.Update(func() {
			if a.voice != call {
				return
			}
			a.voice = nil
			if call.expanded && call.chat != "" {
				a.openConversation(call.chat)
			}
			if err != nil && !call.ending && !errors.Is(err, context.Canceled) {
				a.voiceErr = err.Error()
				var failure *api.HTTPError
				if errors.As(err, &failure) && failure.Status == http.StatusPreconditionFailed {
					a.showConnections()
					a.voiceKeyOpen = true
					a.changeVoiceKey(http.MethodGet)
				}
			}
			a.refreshSoon()
		})
	}()
}

func (a *desktop) backToCallChat(call *desktopVoice) {
	call.expanded = false
	if call.chat != "" {
		a.openConversation(call.chat)
	}
}

func (a *desktop) endVoice() {
	if call := a.voice; call != nil && !call.teamPending {
		call.ending = true
		call.muted.Store(true)
		if call.ready {
			select {
			case call.commands <- api.VoiceEvent{Type: "hangup"}:
				return
			default:
			}
		}
		call.cancel()
	}
}

func (a *desktop) toggleVoiceMute() {
	call := a.voice
	if call == nil || !call.ready || call.ending {
		return
	}
	mute := !call.muted.Load()
	if mute {
		call.muted.Store(true)
		call.micLevel, call.hearing = 0, false
	}
	if !a.commandVoice(api.VoiceEvent{Type: "mute", Muted: mute}) && mute {
		call.muted.Store(false)
	}
}

func (a *desktop) commandVoice(event api.VoiceEvent) bool {
	if call := a.voice; call != nil && !call.ending {
		call.notice = ""
		select {
		case call.commands <- event:
			return true
		default:
			call.notice = "The call is catching up. Try again."
		}
	}
	return false
}

type desktopVoiceInput struct {
	event api.VoiceEvent
	pcm   []byte
	err   error
}

type callAudio interface {
	Play([]byte) bool
	Running() bool
	Drained() bool
	Reset() int
	Close()
}

type callAudioDevice struct {
	permission func(context.Context) (bool, error)
	open       func(context.Context, chan<- []byte, *atomic.Bool, bool) (callAudio, error)
}

type callAudioLink interface {
	Send(context.Context, api.VoiceEvent) error
	Audio(context.Context, []byte) error
}

func runDesktopVoice(ctx context.Context, client *api.Client, call *desktopVoice, chat, agent string, direct bool, update func(api.VoiceEvent)) (callErr error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	callID := "connecting"
	logVoice := func(format string, args ...any) {
		log.Printf("voice call=%s "+format, append([]any{callID}, args...)...)
	}
	defer func() {
		if callErr != nil && !errors.Is(callErr, context.Canceled) {
			logVoice("desktop ended error_type=%T", callErr)
		} else {
			logVoice("desktop ended normally")
		}
	}()
	stopRinging := voiceaudio.Ring()
	defer stopRinging()
	dialCtx, cancelDial := context.WithTimeout(ctx, 10*time.Second)
	remote, err := client.OpenVoice(dialCtx, chat, agent)
	cancelDial()
	if err != nil {
		return err
	}
	defer remote.Close()
	incoming := make(chan desktopVoiceInput, 32)
	go func() {
		for {
			event, pcm, err := remote.Read(ctx)
			select {
			case incoming <- desktopVoiceInput{event, pcm, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	device := callAudioDevice{permission: voiceaudio.RequestPermission,
		open: func(ctx context.Context, capture chan<- []byte, muted *atomic.Bool, direct bool) (callAudio, error) {
			audio, err := voiceaudio.OpenAuthorized(ctx, capture, muted, direct)
			if err != nil {
				return nil, err
			}
			return audio, nil
		}}
	return relayDesktopVoice(ctx, remote, incoming, call, direct, device, stopRinging, &callID, update)
}

func relayDesktopVoice(ctx context.Context, remote callAudioLink, incoming <-chan desktopVoiceInput, call *desktopVoice, direct bool,
	device callAudioDevice, stopRinging func(), callID *string, update func(api.VoiceEvent)) error {
	logVoice := func(format string, args ...any) {
		log.Printf("voice call=%s "+format, append([]any{*callID}, args...)...)
	}
	var capture chan []byte
	var audio callAudio
	defer func() {
		if audio != nil {
			audio.Close()
		}
	}()
	tick := time.NewTicker(45 * time.Millisecond)
	defer tick.Stop()
	var item string
	var announce api.VoiceEvent
	var bootstrap api.VoiceEvent
	var audioEnd, captureStart, lastCapture, lastHealthCheck, lastMeter, echoUntil, reopenAt, checkSignalAt time.Time
	var bufferedPCM []byte
	const maxBufferedPCM = 24000 * 2 * 45 // Match the native player's 45-second limit.
	var micPeak, micMeter float32
	var gotPacket, gotSignal, warnedSilence bool
	var waitingForAudio bool
	openAttempts := 0
	const maxMicAttempts = 3
	retryMic := func(reason string, nextDirect bool) {
		logVoice("microphone retry reason=%s attempt=%d", reason, openAttempts)
		if audio != nil {
			audio.Close()
			audio = nil
		}
		capture = nil
		direct = nextDirect
		reopenAt = time.Now().Add(300 * time.Millisecond)
		update(api.VoiceEvent{Type: "opening_audio", Text: "Reconnecting microphone…"})
	}
	openMic := func() error {
		openAttempts++
		reopenAt = time.Time{}
		// A callback from the old device must not mark the new device ready.
		capture = make(chan []byte, 32)
		var err error
		audio, err = device.open(ctx, capture, &call.muted, direct)
		if errors.Is(err, voiceaudio.ErrDevice) && openAttempts < maxMicAttempts {
			retryMic("device_open", true)
			return nil
		}
		if err == nil {
			captureStart = time.Now()
			lastCapture = captureStart
			checkSignalAt = captureStart.Add(5 * time.Second)
			gotPacket, gotSignal, warnedSilence = false, false, false
			micPeak, micMeter = 0, 0
			logVoice("microphone opened direct=%t attempt=%d", direct, openAttempts)
			if direct {
				update(api.VoiceEvent{Type: "direct_mic"})
			} else {
				update(api.VoiceEvent{Type: "duplex_mic"})
			}
			if len(bufferedPCM) > 0 {
				if !audio.Play(bufferedPCM) {
					return errors.New("Buddy couldn't play the call audio after reconnecting.")
				}
				bufferedPCM = nil
				if !audioEnd.IsZero() {
					audioEnd = time.Now()
				}
				if announce.Type != "" {
					update(announce)
					announce = api.VoiceEvent{}
				}
			}
		}
		return err
	}
	var lastCaption time.Time
	var caption api.VoiceEvent
	mutedOnServer := false
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case command := <-call.commands:
			if command.Type == "hangup" {
				if err := remote.Send(ctx, command); err != nil {
					logVoice("hangup relay failed error_type=%T", err)
				}
				return nil
			}
			if command.Type == "mute" {
				mutedOnServer = command.Muted
				// Drop packets captured just before the mic was muted.
				for range len(capture) {
					<-capture
				}
			}
			if err := remote.Send(ctx, command); err != nil {
				return errors.New("The voice connection was lost.")
			}
			if command.Type == "mute" {
				lastCapture = time.Now()
				micPeak, micMeter = 0, 0
				if !command.Muted {
					checkSignalAt = lastCapture.Add(5 * time.Second)
					call.muted.Store(false)
				}
				update(api.VoiceEvent{Type: "mic_level"})
			}
		case pcm := <-capture:
			if audio == nil {
				continue
			}
			now := time.Now()
			lastCapture = now
			if !gotPacket {
				gotPacket = true
				logVoice("first microphone packet direct=%t wait=%s", direct, now.Sub(captureStart).Round(time.Millisecond))
				update(api.VoiceEvent{Type: "opening_audio", Text: "Checking microphone signal…"})
			}
			if bootstrap.Type != "" {
				if err := remote.Send(ctx, api.VoiceEvent{Type: "audio_ready"}); err != nil {
					return errors.New("The voice connection was lost.")
				}
				update(bootstrap)
				bootstrap = api.VoiceEvent{}
			}
			if !mutedOnServer && !call.muted.Load() {
				level := voiceaudio.Level(pcm)
				micPeak = max(micPeak, level)
				if !gotSignal && item == "" && !now.Before(echoUntil) && len(bytes.Trim(pcm, "\x00")) > 0 {
					gotSignal = true
					openAttempts = 1 // A healthy device earns a fresh retry budget if it later disconnects.
					logVoice("first microphone signal direct=%t", direct)
					update(api.VoiceEvent{Type: "mic_ready"})
				}
				// The direct device has no playback reference. Wait for the
				// speaker to drain so the agent can't hear itself.
				if !direct || item == "" && !now.Before(echoUntil) {
					if err := remote.Audio(ctx, pcm); err != nil {
						return errors.New("The voice connection was lost.")
					}
				}
			}
		case in := <-incoming:
			if in.err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				logVoice("connection read ended error_type=%T", in.err)
				return errors.New("The call ended. Completed captions are in the chat.")
			}
			if in.pcm != nil {
				if item == "" {
					return errors.New("Unexpected call audio.")
				}
				if audio == nil && !reopenAt.IsZero() {
					if len(in.pcm) > maxBufferedPCM-len(bufferedPCM) {
						return errors.New("Call audio stopped playing while the microphone reconnected.")
					}
					bufferedPCM = append(bufferedPCM, in.pcm...)
					continue
				}
				if audio == nil || !audio.Play(in.pcm) {
					return errors.New("Buddy couldn't play the call audio. Check your sound output and try again.")
				}
				if announce.Type != "" {
					update(announce)
					announce = api.VoiceEvent{}
				}
				continue
			}
			event := in.event
			switch event.Type {
			case "error":
				if event.Call != "" {
					*callID = event.Call
				}
				logVoice("server ended call")
				return errors.New(event.Text)
			case "ready":
				if audio != nil || bootstrap.Type != "" {
					return errors.New("The voice connection restarted unexpectedly.")
				}
				stopRinging()
				if event.Call != "" {
					*callID = event.Call
				}
				update(api.VoiceEvent{Type: "opening_audio", Text: "Checking microphone access…"})
				fresh, err := device.permission(ctx)
				if err != nil {
					logVoice("microphone permission unavailable")
					return err
				}
				direct = direct || fresh
				logVoice("microphone permission granted fresh=%t", fresh)
				update(api.VoiceEvent{Type: "opening_audio", Text: "Connecting microphone…"})
				bootstrap = event
				if fresh {
					// Give macOS a moment to finish bringing up the newly authorized device.
					reopenAt = time.Now().Add(250 * time.Millisecond)
				} else if err := openMic(); err != nil {
					return err
				}
				continue
			case "audio_start":
				if audio == nil && reopenAt.IsZero() || event.Item == "" || item != "" {
					return errors.New("Unexpected call audio.")
				}
				item, audioEnd, announce = event.Item, time.Time{}, event
				continue
			case "audio_end":
				if event.Item == item {
					audioEnd = time.Now()
				}
			case "interrupt":
				waitingForAudio = false
				echoUntil = time.Now().Add(220 * time.Millisecond)
				checkSignalAt = echoUntil.Add(5 * time.Second)
				caption, announce = api.VoiceEvent{}, api.VoiceEvent{}
				if item != "" && event.Item == item {
					var ms int
					if audio != nil {
						ms = audio.Reset()
					}
					bufferedPCM = nil
					item, audioEnd = "", time.Time{}
					if err := remote.Send(ctx, api.VoiceEvent{Type: "cut", Item: event.Item, Millis: ms}); err != nil {
						return errors.New("The voice connection was lost.")
					}
				} else {
					// A speech-start can cross a playback receipt on the wire.
					if err := remote.Send(ctx, api.VoiceEvent{Type: "cut", Item: event.Item, Millis: 60000}); err != nil {
						return errors.New("The voice connection was lost.")
					}
				}
			case "caption":
				caption = event
				continue
			case "listening":
				caption = api.VoiceEvent{}
				if item == "" {
					waitingForAudio = false
				}
			case "responding":
				caption = api.VoiceEvent{}
				waitingForAudio = true
			case "settled", "idle":
				waitingForAudio = false
				checkSignalAt = time.Now().Add(5 * time.Second)
			case "final":
				if event.Speaker != "You" {
					caption = api.VoiceEvent{}
				}
			}
			update(event)
		case now := <-tick.C:
			if !reopenAt.IsZero() && !now.Before(reopenAt) {
				if err := openMic(); err != nil {
					return err
				}
			}
			if audio != nil && now.Sub(lastHealthCheck) >= time.Second {
				lastHealthCheck = now
				micActive := !mutedOnServer && !call.muted.Load()
				noPackets := micActive && now.Sub(lastCapture) > 3*time.Second
				running := audio.Running()
				noSignal := micActive && gotPacket && !gotSignal && item == "" && !waitingForAudio && !now.Before(checkSignalAt)
				if item == "" && !waitingForAudio && (!running || noPackets || noSignal) && openAttempts < maxMicAttempts {
					reason := "no_signal"
					if noPackets {
						reason = "no_packets"
					} else if !running {
						reason = "device_stopped"
					}
					retryMic(reason, reason != "no_signal" || !direct)
				} else if !running && (item != "" || openAttempts == maxMicAttempts) {
					return errors.New("Your audio device disconnected. Check your Mac’s sound settings, then call again.")
				} else if noPackets && openAttempts == maxMicAttempts {
					return errors.New("Buddy isn’t receiving microphone audio. Try Test microphone in Connections → Live voice.")
				} else if noSignal && openAttempts == maxMicAttempts && !warnedSilence {
					warnedSilence = true
					logVoice("microphone silent after %d attempts", openAttempts)
					update(api.VoiceEvent{Type: "opening_audio", Text: "No microphone signal. Check your sound input."})
				}
			}
			if audio != nil && !mutedOnServer && !call.muted.Load() && now.Sub(lastMeter) >= 90*time.Millisecond {
				lastMeter = now
				micMeter = max(micPeak, micMeter*.65)
				update(api.VoiceEvent{Type: "mic_level", Level: micMeter})
				micPeak = 0
			}
			if caption.Type != "" && now.Sub(lastCaption) >= 90*time.Millisecond {
				update(caption)
				caption, lastCaption = api.VoiceEvent{}, now
			}
			if audio != nil && item != "" && !audioEnd.IsZero() {
				if audio.Drained() {
					audio.Reset()
					if err := remote.Send(ctx, api.VoiceEvent{Type: "played", Item: item}); err != nil {
						return errors.New("The voice connection was lost.")
					}
					item, audioEnd = "", time.Time{}
					waitingForAudio = false
					echoUntil = now.Add(220 * time.Millisecond)
					checkSignalAt = echoUntil.Add(5 * time.Second)
					update(api.VoiceEvent{Type: "settled"})
				} else if now.Sub(audioEnd) > 50*time.Second {
					return errors.New("Call audio stopped playing. Check your sound output and try again.")
				}
			}
		}
	}
}

func (a *desktop) changeVoiceKey(method string) {
	if a.window == nil || !a.online || a.voiceKeyBusy {
		return
	}
	key := strings.TrimSpace(a.voiceKeyDraft)
	if method == http.MethodPut {
		if key == "" {
			return
		}
		a.voiceKeyDraft = ""
	}
	a.voiceKeyBusy, a.voiceKeyErr = true, ""
	client, win := a.client, a.window
	go func() {
		status, err := client.VoiceKey(a.ctx, method, key)
		win.Update(func() {
			if a.client != client {
				return
			}
			a.voiceKeyBusy = false
			if err != nil {
				a.voiceKeyErr = errorMessage(err, "Couldn't load voice settings.")
				return
			}
			a.voiceAuth, a.voiceAuthLoaded = status, true
			if method == http.MethodPut {
				a.voiceErr, a.voiceKeyOpen = "", false
			}
		})
	}()
}

func (call *desktopVoice) memberName(id string) string {
	for _, member := range call.members {
		if member.ID == id {
			return member.Name
		}
	}
	return ""
}

func (call *desktopVoice) contains(id string) bool {
	return slices.ContainsFunc(call.members, func(member api.VoiceMember) bool { return member.ID == id })
}
