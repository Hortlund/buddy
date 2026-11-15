package budmodel

import (
	"bytes"
	"image/color"
	"testing"
	"time"
)

func TestWorkPropEmergesAndReturns(t *testing.T) {
	for _, action := range []Action{Investigate, Notebook, Idea} {
		pose := action.pose(action.duration() * .51)
		if pose.values[11] < .8 {
			t.Fatalf("action %d never displayed its prop", action)
		}
		var r Renderer
		body, eyes := color.RGBA{255, 136, 86, 255}, color.RGBA{45, 25, 26, 255}
		withProp := bytes.Clone(r.Draw(120, "orb", body, eyes, pose, false).Pix)
		pose.values[11] = 0
		if bytes.Equal(withProp, r.Draw(120, "orb", body, eyes, pose, false).Pix) {
			t.Fatalf("action %d drew its prop entirely behind the body", action)
		}
		if pose := action.pose(action.duration()); pose != (Pose{}) {
			t.Fatalf("action %d retained a prop after finishing", action)
		}
	}
}

func TestDirectorVariesWorkAndKeepsTurnsShort(t *testing.T) {
	now := time.Unix(100, 0)
	var d Director
	seen := make(map[Action]bool)
	for frame := range 600 {
		pose := d.Sample(now.Add(time.Duration(frame)*32*time.Millisecond), Investigate, 0, 0, false)
		seen[pose.Action] = true
		if abs(pose.values[0]) > .75 {
			t.Fatalf("unexpected full-body rotation at frame %d", frame)
		}
	}
	if !seen[Investigate] || len(seen) < 3 {
		t.Fatalf("missing authored action or pause: %v", seen)
	}
}

func TestTwirlReturnsToRest(t *testing.T) {
	mid := Twirl.pose(.74)
	if mid.values[0] < 2 || mid.values[0] > 5 || mid.values[3] <= 0 {
		t.Fatalf("twirl did not turn in the air: %v", mid)
	}
	if pose := Twirl.pose(Twirl.duration()); pose != (Pose{}) {
		t.Fatalf("twirl did not return to rest: %v", pose)
	}
}
