//go:build darwin && cgo

package voiceaudio

/*
#cgo CFLAGS: -fobjc-arc -fblocks
#cgo LDFLAGS: -framework AVFoundation -framework AVFAudio -framework Foundation -framework CoreMedia
#include "audio.h"
*/
import "C"

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"unsafe"
)

var tokens atomic.Uint64
var permissions sync.Map
var inputs sync.Map

// Audio is used serially by one call. Capture is delivered on a bounded channel.
type Audio struct {
	native unsafe.Pointer
	token  uint64
}

func Available() bool { return true }

// OpenAuthorized starts a call device after RequestPermission has succeeded.
func OpenAuthorized(ctx context.Context, capture chan<- []byte, muted *atomic.Bool, direct bool) (*Audio, error) {
	return open(ctx, capture, muted, direct, true)
}

func OpenMicCheck(ctx context.Context, capture chan<- []byte) (*Audio, error) {
	if _, err := RequestPermission(ctx); err != nil {
		return nil, err
	}
	return open(ctx, capture, nil, true, false)
}

type permissionResult struct{ allowed, prompted bool }

// RequestPermission waits for macOS to resolve the prompt. A fresh grant uses
// direct capture first, avoiding the duplex route during its first setup.
func RequestPermission(ctx context.Context) (bool, error) {
	token := tokens.Add(1)
	result := make(chan permissionResult, 1)
	permissions.Store(token, result)
	defer permissions.Delete(token)
	C.buddy_voice_permission(C.uint64_t(token))
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case permission := <-result:
		if !permission.allowed {
			return false, errors.New("Allow Buddy to use your microphone in System Settings → Privacy & Security → Microphone.")
		}
		return permission.prompted, ctx.Err()
	}
}

var audioDevice = make(chan struct{}, 1)

func open(ctx context.Context, capture chan<- []byte, muted *atomic.Bool, direct, playback bool) (*Audio, error) {
	// The microphone test and a call may overlap while the test is shutting down.
	select {
	case audioDevice <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		<-audioDevice
		return nil, err
	}
	token := tokens.Add(1)
	inputs.Store(token, audioInput{capture, muted})
	ptr := C.buddy_voice_open(C.uint64_t(token), C.bool(direct), C.bool(playback))
	if ptr == nil {
		inputs.Delete(token)
		<-audioDevice
		if !playback {
			return nil, errors.New("Couldn’t open your Mac’s selected microphone.")
		}
		return nil, ErrDevice
	}
	return &Audio{ptr, token}, nil
}

type audioInput struct {
	capture chan<- []byte
	muted   *atomic.Bool
}

//export buddyVoicePermission
func buddyVoicePermission(token C.uint64_t, allowed, prompted C.int) {
	if value, ok := permissions.Load(uint64(token)); ok {
		select {
		case value.(chan permissionResult) <- permissionResult{allowed != 0, prompted != 0}:
		default:
		}
	}
}

//export buddyVoicePCM
func buddyVoicePCM(token C.uint64_t, pcm unsafe.Pointer, count C.int) {
	value, ok := inputs.Load(uint64(token))
	if !ok || count <= 0 || count > 1920 {
		return
	}
	in := value.(audioInput)
	if in.muted != nil && in.muted.Load() {
		return
	}
	select {
	case in.capture <- C.GoBytes(pcm, count):
	default: // The audio callback must never wait for the network.
	}
}

func (a *Audio) Play(pcm []byte) bool {
	return len(pcm) > 0 && len(pcm)%2 == 0 && bool(C.buddy_voice_play(a.native, unsafe.Pointer(&pcm[0]), C.int(len(pcm)/2)))
}

func (a *Audio) Running() bool { return bool(C.buddy_voice_running(a.native)) }

func (a *Audio) Drained() bool { return bool(C.buddy_voice_drained(a.native)) }
func (a *Audio) Reset() int    { return int(C.buddy_voice_reset(a.native)) }

func (a *Audio) Close() {
	if a.native == nil {
		return
	}
	inputs.Delete(a.token)
	C.buddy_voice_close(a.native)
	a.native = nil
	<-audioDevice
}

// Ring plays a soft call cue through the system output, without opening the mic.
func Ring() func() {
	wave := ringWave()
	ptr := C.buddy_voice_ring(unsafe.Pointer(&wave[0]), C.int(len(wave)))
	return sync.OnceFunc(func() {
		if ptr != nil {
			C.buddy_voice_stop_ring(ptr)
		}
	})
}
