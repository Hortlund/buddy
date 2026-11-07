//go:build !darwin || !cgo

package voiceaudio

import (
	"context"
	"errors"
	"sync/atomic"
)

type Audio struct{}

func Available() bool { return false }

func RequestPermission(context.Context) (bool, error) {
	return false, errors.New("Live audio requires the native macOS app.")
}

func OpenAuthorized(context.Context, chan<- []byte, *atomic.Bool, bool) (*Audio, error) {
	return nil, errors.New("Live audio requires the native macOS app.")
}

func OpenMicCheck(context.Context, chan<- []byte) (*Audio, error) {
	return nil, errors.New("Microphone testing requires the native macOS app.")
}

func (*Audio) Play([]byte) bool { return false }
func (*Audio) Running() bool    { return false }
func Ring() func()              { return func() {} }
func (*Audio) Drained() bool    { return true }
func (*Audio) Reset() int       { return 0 }
func (*Audio) Close()           {}
