package main

import (
	"context"
	"time"

	"buddy/internal/voiceaudio"
	"github.com/egoist/mygo/ui"
)

func (a *desktop) startMicCheck() {
	if a.micCheckCancel != nil || a.voice != nil || a.window == nil || !voiceaudio.Available() {
		return
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.micCheckCancel = cancel
	a.micCheckLevel = 0
	a.micCheckStatus = "Opening microphone…"
	win := a.window
	go func() {
		defer cancel()
		capture := make(chan []byte, 32)
		audio, err := voiceaudio.OpenMicCheck(ctx, capture)
		if err != nil {
			status := err.Error()
			if ctx.Err() != nil {
				status = ""
			}
			win.Update(func() {
				a.micCheckCancel = nil
				a.micCheckStatus = status
			})
			return
		}
		win.Update(func() { a.micCheckStatus = "Say a few words to check the input level." })
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		timeout := time.NewTimer(8 * time.Second)
		defer timeout.Stop()
		var peak, level float32
		status := "No signal detected. Check the selected input in System Settings → Sound."
	loop:
		for {
			select {
			case <-ctx.Done():
				status = ""
				break loop
			case <-timeout.C:
				break loop
			case pcm := <-capture:
				level = max(level, voiceaudio.Level(pcm))
				peak = max(peak, level)
			case <-tick.C:
				current := level
				level = 0
				win.Update(func() { a.micCheckLevel = current })
			}
		}
		audio.Close()
		detected := status != "" && peak >= .10
		if detected {
			status = "Microphone detected. Buddy will use this input mode for your next call."
		}
		win.Update(func() {
			a.micCheckCancel = nil
			a.micCheckLevel = peak
			a.micCheckStatus = status
			if detected {
				a.directVoiceMic = true
			}
		})
	}()
}

func (a *desktop) micCheckControls(c *ui.Context) {
	if !voiceaudio.Available() {
		return
	}
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		if a.micCheckCancel == nil {
			ui.Button(c.Key("test-microphone"), "Test microphone").Height(29).
				Disabled(a.voice != nil).OnClick(a.startMicCheck)
		} else {
			ui.Button(c.Key("stop-microphone-test"), "Stop test").Height(29).OnClick(a.micCheckCancel)
		}
		if a.micCheckCancel != nil || a.micCheckLevel > 0 {
			ui.Box(c).Width(58).Height(5).Radius(3).Background(c.Theme().SurfacePressed).Children(func() {
				ui.Box(c).Width(58 * a.micCheckLevel).Height(5).Radius(3).Background(c.Theme().Text)
			})
		}
		text := a.micCheckStatus
		if text == "" {
			text = "Check input locally before a call."
		}
		ui.Text(c, text).Grow(1).MinWidth(0).FontSize(12).TextColor(c.Theme().TextMuted)
	})
}
