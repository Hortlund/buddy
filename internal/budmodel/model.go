// Package budmodel loads the Bud geometry and authored poses exported from Blender.
package budmodel

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"math"
	"math/rand/v2"
	"time"
)

//go:embed Bud.mesh
var modelData []byte

type vector struct{ x, y, z float32 }

func (v vector) add(w vector) vector  { return vector{v.x + w.x, v.y + w.y, v.z + w.z} }
func (v vector) dot(w vector) float32 { return v.x*w.x + v.y*w.y + v.z*w.z }

type vertex struct{ position, normal vector }
type triangle struct {
	a, b, c  uint16
	material uint8
}
type mesh struct {
	vertices []vertex
	faces    []triangle
}

const (
	shapeCount = 7
	crest      = shapeCount
	eye        = crest + 1
	loupe      = eye + 1
	book       = loupe + 1
	bulb       = book + 1
	teacup     = bulb + 1
	pencil     = teacup + 1
	headphones = pencil + 1
	envelope   = headphones + 1
	telescope  = envelope + 1
	meshCount  = telescope + 1
)

type Action uint8

const (
	Rest Action = iota
	Look
	Hop
	Think
	Investigate
	Notebook
	Idea
	Nod
	Talk
	Twirl
	DoubleHop
	Tilt
	Tea
	Sketch
	Music
	Letter
	Telescope
	Sway
	Stretch
	Sleepy
	Peek
	LookUp
	Settle
)

type Pose struct {
	values [13]float32 // yaw, pitch, roll, lift, squash, blink, gaze X/Y, prop XYZ, scale and tilt
	Action Action
}

type key struct {
	second float32
	values [13]float32
}

type model struct {
	eyes   [shapeCount][2]vector
	meshes [meshCount]mesh
	clips  [Settle + 1][]key
}

var bud = readModel()

func readModel() model {
	if string(modelData[:4]) != "BUD5" {
		panic("invalid Bud model header")
	}
	var result model
	r := bytes.NewReader(modelData[4:])
	read := func(out any) {
		if err := binary.Read(r, binary.LittleEndian, out); err != nil {
			panic(err)
		}
	}
	var counts [3]uint8
	read(&counts)
	if counts != [3]uint8{shapeCount, meshCount, uint8(Settle) + 1} {
		panic("unexpected Bud model contents")
	}
	for i := range result.eyes {
		for j := range result.eyes[i] {
			var v [3]float32
			read(&v)
			result.eyes[i][j] = vector{v[0], v[1], v[2]}
		}
	}
	for i := range result.meshes {
		var counts [2]uint16
		read(&counts)
		part := &result.meshes[i]
		part.vertices = make([]vertex, counts[0])
		part.faces = make([]triangle, counts[1])
		for j := range part.vertices {
			var p [3]int16
			var n [3]int16
			read(&p)
			read(&n)
			part.vertices[j] = vertex{
				vector{float32(p[0]) / 12000, float32(p[1]) / 12000, float32(p[2]) / 12000},
				vector{float32(n[0]) / 32767, float32(n[1]) / 32767, float32(n[2]) / 32767},
			}
		}
		for j := range part.faces {
			var idx [3]uint16
			var material uint8
			read(&idx)
			read(&material)
			part.faces[j] = triangle{idx[0], idx[1], idx[2], material}
		}
	}
	for action := Look; action <= Settle; action++ {
		var count uint8
		read(&count)
		result.clips[action] = make([]key, count)
		for i := range result.clips[action] {
			k := &result.clips[action][i]
			read(&k.second)
			read(&k.values)
		}
	}
	if r.Len() != 0 {
		panic("trailing Bud model data")
	}
	return result
}

func shapeIndex(name string) int {
	for i, shape := range [...]string{"orb", "bean", "pebble", "drop", "cloud", "capsule", "pillow"} {
		if name == shape {
			return i
		}
	}
	return 0
}

func smooth(t float32) float32 { return t * t * t * (10 + t*(-15+6*t)) }

func (a Action) duration() float32 {
	if a == Rest {
		return 0
	}
	keys := bud.clips[a]
	return keys[len(keys)-1].second
}

func (a Action) pose(t float32) Pose {
	if a == Rest || t >= a.duration() {
		return Pose{}
	}
	keys := bud.clips[a]
	for i := 1; i < len(keys); i++ {
		if t <= keys[i].second {
			from, to := keys[i-1], keys[i]
			u := smooth(max(0, (t-from.second)/(to.second-from.second)))
			pose := Pose{Action: a}
			for j, v := range from.values {
				pose.values[j] = v + (to.values[j]-v)*u
			}
			return pose
		}
	}
	return Pose{}
}

// Director plays whole gestures and changes activities at pose boundaries.
// Pointer gaze is blended in after the authored gesture.
type Director struct {
	started, previous, next, blink time.Time
	action, requested              Action
	requestChanged                 bool
	recent                         [3]Action
	lastPlayful                    time.Time
	yaw, pitch, tempo, intensity   float32
	breathPhase                    float64
	mirror                         bool
}

func (d *Director) Sample(now time.Time, desired Action, aimX, aimY float32, small bool) Pose {
	if d.previous.IsZero() {
		d.next = now.Add(time.Duration(7500+rand.IntN(6500)) * time.Millisecond)
		d.blink = now.Add(time.Duration(2200+rand.IntN(2800)) * time.Millisecond)
		d.lastPlayful = now
		d.breathPhase = rand.Float64() * 2 * math.Pi
	}
	dt := float32(max(0, min(.12, now.Sub(d.previous).Seconds())))
	d.previous = now
	if desired != d.requested {
		wasRest := d.requested == Rest
		d.requested, d.requestChanged = desired, true
		if desired != Rest && (d.started.IsZero() || wasRest) {
			ready := now.Add(time.Duration(650+rand.IntN(350)) * time.Millisecond)
			if !d.started.IsZero() {
				settled := d.started.Add(time.Duration(d.action.duration()/d.tempo*1000+2500) * time.Millisecond)
				if settled.After(ready) {
					ready = settled
				}
			}
			if ready.Before(d.next) {
				d.next = ready
			}
		}
	}
	factor := 1 - float32(math.Exp(float64(-dt*8.5)))
	d.yaw += (aimX - d.yaw) * factor
	d.pitch += (aimY - d.pitch) * factor

	age := float32(now.Sub(d.started).Seconds()) * d.tempo
	if age >= d.action.duration() && !now.Before(d.next) {
		next := desired
		if next == Rest || !d.requestChanged {
			next = d.choose(now, desired)
		}
		d.action, d.started = next, now
		d.recent = [3]Action{next, d.recent[0], d.recent[1]}
		d.requestChanged = false
		d.tempo = .74 + rand.Float32()*.16
		d.intensity = .80 + rand.Float32()*.20
		d.mirror = (next == Look || next == Hop || next == Think || next == Talk ||
			next == Twirl || next == DoubleHop || next == Tilt || next >= Sway) && rand.IntN(2) == 0
		pause := 7500 + rand.IntN(7500)
		if desired != Rest {
			pause = 3800 + rand.IntN(4700)
		}
		if small {
			pause += 2500 + rand.IntN(3500)
		}
		d.next = now.Add(time.Duration(float32(pause)+next.duration()*1000/d.tempo) * time.Millisecond)
		age = 0
	}
	pose := d.action.pose(age)
	for i := range 5 {
		if pose.Action != Twirl || i != 0 {
			pose.values[i] *= d.intensity
		}
	}
	if d.mirror {
		v := &pose.values
		v[0], v[2], v[6] = -v[0], -v[2], -v[6]
	}
	gaze := float32(1)
	if pose.Action == Think || pose.values[11] > .01 {
		gaze = .38
	}
	pose.values[0] += d.yaw * gaze * .59
	pose.values[1] += d.pitch * gaze * .35
	phase := float64(now.UnixMilli()%8500)*2*math.Pi/8500 + d.breathPhase
	pose.values[3] += float32(math.Sin(phase)) * .008
	pose.values[4] += float32(math.Sin(phase)) * .005
	if blinkAge := now.Sub(d.blink).Seconds(); blinkAge >= 0 {
		if blinkAge >= .24 {
			d.blink = now.Add(time.Duration(2800+rand.IntN(4700)) * time.Millisecond)
		} else {
			pose.values[5] = max(pose.values[5], float32(math.Sin(blinkAge*math.Pi/.24)))
		}
	}
	return pose
}

func (d *Director) choose(now time.Time, desired Action) Action {
	if desired == Rest && now.Sub(d.lastPlayful) > 75*time.Second && rand.IntN(12) == 0 {
		d.lastPlayful = now
		return [...]Action{Hop, Twirl, DoubleHop}[rand.IntN(3)]
	}
	var pool []Action
	switch desired {
	case Rest:
		pool = []Action{Look, Tilt, Sway, Stretch, Sleepy, Peek, LookUp, Settle, Tea, Music, Notebook}
	case Think:
		pool = []Action{Think, Tilt, LookUp, Notebook, Sketch, Settle, Nod}
	case Investigate:
		pool = []Action{Investigate, Telescope, Look, Peek, Think, Letter}
	case Notebook:
		pool = []Action{Notebook, Sketch, Letter, Think, LookUp, Settle}
	case Idea:
		pool = []Action{Idea, Letter, Sketch, Think, Nod, LookUp}
	default:
		pool = []Action{desired, Tilt, Nod, Look, Sway, Settle}
	}
	candidates := make([]Action, 0, len(pool))
	for _, action := range pool {
		if action != d.recent[0] && action != d.recent[1] && action != d.recent[2] {
			candidates = append(candidates, action)
		}
	}
	return candidates[rand.IntN(len(candidates))]
}

func abs(x float32) float32 { return max(x, -x) }
