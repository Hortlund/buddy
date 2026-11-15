package budmodel

import (
	"bytes"
	"image/color"
	"testing"
	"time"
)

func TestWorkPropEmergesAndReturns(t *testing.T) {
	for _, action := range []Action{Investigate, Notebook, Idea, Tea, Sketch, Music, Letter, Telescope} {
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
	for frame := range 4000 {
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
	mid := Twirl.pose(Twirl.duration() * .43)
	if mid.values[0] < 2 || mid.values[0] > 5 || mid.values[3] <= 0 {
		t.Fatalf("twirl did not turn in the air: %v", mid)
	}
	if pose := Twirl.pose(Twirl.duration()); pose != (Pose{}) {
		t.Fatalf("twirl did not return to rest: %v", pose)
	}
}

func TestDirectorKeepsQuietGapsAcrossWorkChanges(t *testing.T) {
	now := time.Unix(100, 0)
	var director Director
	var previousStart time.Time
	var previousDuration float32
	seen := make(map[Action]bool)
	for frame := range 6000 {
		at := now.Add(time.Duration(frame) * 100 * time.Millisecond)
		desired := [...]Action{Think, Investigate, Notebook, Idea}[(frame/9)%4]
		director.Sample(at, desired, 0, 0, false)
		if director.started.IsZero() || director.started == previousStart {
			continue
		}
		if !previousStart.IsZero() && at.Sub(previousStart).Seconds()-float64(previousDuration) < 3.7 {
			t.Fatalf("work change interrupted the quiet interval after %v", director.action)
		}
		previousStart = director.started
		previousDuration = director.action.duration() / director.tempo
		seen[director.action] = true
	}
	if len(seen) < 4 {
		t.Fatalf("work activities lost their variation: %v", seen)
	}
}

func TestQuietIdleStartsSlowlyAndAvoidsEarlyAcrobatics(t *testing.T) {
	now := time.Unix(100, 0)
	var director Director
	for frame := range 750 {
		pose := director.Sample(now.Add(time.Duration(frame)*100*time.Millisecond), Rest, 0, 0, true)
		if frame < 75 && pose.Action != Rest {
			t.Fatal("idle gesture started before the initial quiet interval")
		}
		if pose.Action == Hop || pose.Action == DoubleHop || pose.Action == Twirl {
			t.Fatalf("idle acrobatics started without a cooldown at frame %d", frame)
		}
	}
}

func TestExportedModelHasValidGeometryAndContinuousClips(t *testing.T) {
	for index, mesh := range bud.meshes {
		for _, vertex := range mesh.vertices {
			length := vertex.normal.dot(vertex.normal)
			if length < .95 || length > 1.05 {
				t.Fatalf("mesh %d has an invalid normal", index)
			}
		}
		for _, face := range mesh.faces {
			if int(face.a) >= len(mesh.vertices) || int(face.b) >= len(mesh.vertices) || int(face.c) >= len(mesh.vertices) || face.material >= 8 {
				t.Fatalf("mesh %d has an invalid face", index)
			}
		}
	}
	for action := Look; action <= Settle; action++ {
		keys := bud.clips[action]
		if len(keys) < 3 || keys[0].second != 0 || keys[0].values != ([13]float32{}) {
			t.Fatalf("action %d does not begin at rest", action)
		}
		for i := 1; i < len(keys); i++ {
			if keys[i].second <= keys[i-1].second {
				t.Fatalf("action %d has unordered keys", action)
			}
		}
	}
}
