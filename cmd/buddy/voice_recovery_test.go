package main

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"buddy/internal/api"
)

type testCallLink chan api.VoiceEvent

func (link testCallLink) Send(_ context.Context, event api.VoiceEvent) error {
	link <- event
	return nil
}

func (testCallLink) Audio(context.Context, []byte) error { return nil }

type testCallAudio struct {
	capture chan<- []byte
	direct  bool
	running atomic.Bool
	played  chan []byte
}

func (audio *testCallAudio) Play(pcm []byte) bool {
	audio.played <- bytes.Clone(pcm)
	return audio.running.Load()
}

func (audio *testCallAudio) Running() bool { return audio.running.Load() }
func (*testCallAudio) Drained() bool       { return true }
func (*testCallAudio) Reset() int          { return 0 }
func (audio *testCallAudio) Close()        { audio.running.Store(false) }

type testCallRelay struct {
	ctx      context.Context
	call     *desktopVoice
	incoming chan desktopVoiceInput
	updates  chan api.VoiceEvent
	opened   chan *testCallAudio
	link     testCallLink
}

func withTestCallRelay(t *testing.T, fresh bool, exercise func(*testing.T, testCallRelay)) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
		defer cancel()
		relay := testCallRelay{ctx: ctx, call: &desktopVoice{commands: make(chan api.VoiceEvent, 8)},
			incoming: make(chan desktopVoiceInput, 16), updates: make(chan api.VoiceEvent, 512),
			opened: make(chan *testCallAudio, 3), link: make(testCallLink, 8)}
		device := callAudioDevice{
			permission: func(context.Context) (bool, error) { return fresh, nil },
			open: func(_ context.Context, capture chan<- []byte, _ *atomic.Bool, direct bool) (callAudio, error) {
				audio := &testCallAudio{capture: capture, direct: direct, played: make(chan []byte, 2)}
				audio.running.Store(true)
				relay.opened <- audio
				return audio, nil
			},
		}
		done := make(chan error, 1)
		go func() {
			id := "test"
			done <- relayDesktopVoice(ctx, relay.link, relay.incoming, relay.call, false, device, func() {}, &id,
				func(event api.VoiceEvent) { relay.updates <- event })
		}()
		relay.event("ready", "")
		exercise(t, relay)
		relay.call.commands <- api.VoiceEvent{Type: "hangup"}
		if err := receiveCallTest(t, ctx, done); err != nil {
			t.Fatal(err)
		}
	})
}

func receiveCallTest[T any](t *testing.T, ctx context.Context, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-ctx.Done():
		t.Fatalf("waiting for call event: %v", ctx.Err())
		var zero T
		return zero
	}
}

func (relay testCallRelay) event(kind, item string) {
	relay.incoming <- desktopVoiceInput{event: api.VoiceEvent{Type: kind, Item: item}}
}

func (relay testCallRelay) waitUpdate(t *testing.T, kind, body string) {
	t.Helper()
	for {
		event := receiveCallTest(t, relay.ctx, relay.updates)
		if event.Type == kind && (body == "" || event.Text == body) {
			return
		}
	}
}

func TestCallPlaysReplyReceivedDuringMutedMicReconnect(t *testing.T) {
	withTestCallRelay(t, false, func(t *testing.T, relay testCallRelay) {
		first := receiveCallTest(t, relay.ctx, relay.opened)
		first.capture <- []byte{1, 0}
		relay.waitUpdate(t, "mic_ready", "")
		receiveCallTest(t, relay.ctx, relay.link) // audio_ready
		relay.call.muted.Store(true)
		relay.call.commands <- api.VoiceEvent{Type: "mute", Muted: true}
		receiveCallTest(t, relay.ctx, relay.link)
		first.running.Store(false)
		relay.waitUpdate(t, "opening_audio", "Reconnecting microphone…")

		pcm := []byte{3, 0, 4, 0}
		relay.event("responding", "")
		relay.event("audio_start", "reply")
		relay.incoming <- desktopVoiceInput{pcm: pcm[:2]}
		relay.incoming <- desktopVoiceInput{pcm: pcm[2:]}
		relay.event("audio_end", "reply")
		second := receiveCallTest(t, relay.ctx, relay.opened)
		if played := receiveCallTest(t, relay.ctx, second.played); !bytes.Equal(played, pcm) {
			t.Fatalf("buffered playback = %v, want %v", played, pcm)
		}
		if event := receiveCallTest(t, relay.ctx, relay.link); event.Type != "played" || event.Item != "reply" {
			t.Fatalf("playback receipt = %+v", event)
		}
		relay.call.commands <- api.VoiceEvent{Type: "mute"}
		receiveCallTest(t, relay.ctx, relay.link)
		synctest.Wait()
		time.Sleep(250 * time.Millisecond) // Let the speaker echo tail finish.
		second.capture <- []byte{1, 0}
		relay.waitUpdate(t, "mic_ready", "")
	})
}

func TestFreshMicPermissionRetriesZeroFilledCapture(t *testing.T) {
	withTestCallRelay(t, true, func(t *testing.T, relay testCallRelay) {
		first := receiveCallTest(t, relay.ctx, relay.opened)
		if !first.direct {
			t.Fatal("first permission grant should start on the direct device")
		}
		for range 26 {
			first.capture <- []byte{0, 0}
			time.Sleep(200 * time.Millisecond)
		}
		second := receiveCallTest(t, relay.ctx, relay.opened)
		if second.direct {
			t.Fatal("a silent direct mic should try the duplex device")
		}
		second.capture <- []byte{1, 0}
		relay.waitUpdate(t, "mic_ready", "")
	})
}
