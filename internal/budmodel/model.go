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
	meshCount  = bulb + 1
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
	clips  [Tilt + 1][]key
}

var bud = readModel()

func readModel() model {
	if string(modelData[:4]) != "BUD4" {
		panic("invalid Bud model header")
	}
	var result model
	r := bytes.NewReader(modelData[4:])
	read := func(out any) {
		if err := binary.Read(r, binary.LittleEndian, out); err != nil {
			panic(err)
		}
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
			var n [3]int8
			read(&p)
			read(&n)
			part.vertices[j] = vertex{
				vector{float32(p[0]) / 12000, float32(p[1]) / 12000, float32(p[2]) / 12000},
				vector{float32(n[0]) / 127, float32(n[1]) / 127, float32(n[2]) / 127},
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
	for action := Look; action <= Tilt; action++ {
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

func smooth(t float32) float32 { return t * t * (3 - 2*t) }

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
	action, requested, lastPlayed  Action
	lastAccent                     Action
	requestChanged                 bool
	yaw, pitch, tempo              float32
	mirror                         bool
}

func (d *Director) Sample(now time.Time, desired Action, aimX, aimY float32, small bool) Pose {
	if d.previous.IsZero() {
		d.next = now.Add(2200*time.Millisecond + time.Duration(rand.IntN(1600))*time.Millisecond)
		d.blink = now.Add(1700*time.Millisecond + time.Duration(rand.IntN(1100))*time.Millisecond)
	}
	dt := float32(max(0, min(.12, now.Sub(d.previous).Seconds())))
	d.previous = now
	if desired != d.requested {
		d.requested, d.requestChanged = desired, true
		d.next = now
	}
	factor := min(1, dt*11)
	d.yaw += (aimX - d.yaw) * factor
	d.pitch += (aimY - d.pitch) * factor

	age := float32(now.Sub(d.started).Seconds()) * d.tempo
	if age >= d.action.duration() && !now.Before(d.next) {
		next := desired
		if next == Rest || (!d.requestChanged && next == d.lastPlayed) {
			if abs(d.yaw) >= .38 {
				next = Look
			} else {
				var pool []Action
				switch {
				case desired == Rest && small:
					pool = []Action{Tilt, Look, Hop, Twirl, Nod, DoubleHop}
				case desired == Rest:
					pool = []Action{Look, Tilt, Hop, DoubleHop, Nod, Twirl}
				case desired == Think || desired == Idea:
					pool = []Action{Tilt, Look, Hop, Twirl, Nod, DoubleHop}
				default:
					pool = []Action{Tilt, Nod, Look, Hop}
				}
				pick := rand.IntN(len(pool))
				if pool[pick] == d.lastAccent {
					pick = (pick + 1 + rand.IntN(len(pool)-1)) % len(pool)
				}
				next, d.lastAccent = pool[pick], pool[pick]
			}
		}
		d.action, d.lastPlayed, d.started = next, next, now
		d.requestChanged = false
		d.tempo = .90 + rand.Float32()*.22
		d.mirror = (next == Look || next == Hop || next == Think || next == Talk || next >= Twirl) && rand.IntN(2) == 0
		pause := 1750 + rand.IntN(2600)
		if desired != Rest {
			pause = 390 + rand.IntN(720)
		}
		d.next = now.Add(time.Duration(float32(pause)+next.duration()*1000/d.tempo) * time.Millisecond)
		age = 0
	}
	pose := d.action.pose(age)
	if d.mirror {
		v := &pose.values
		v[0], v[2], v[6] = -v[0], -v[2], -v[6]
	}
	gaze := float32(1)
	if pose.Action >= Think && pose.Action <= Idea {
		gaze = .38
	}
	pose.values[0] += d.yaw * gaze * .59
	pose.values[1] += d.pitch * gaze * .35
	phase := float64(now.UnixMilli()%11000) * 2 * math.Pi / 11000
	pose.values[3] += float32(math.Sin(phase*3)) * .020
	pose.values[4] += float32(math.Sin(phase*3)) * .016
	if blinkAge := now.Sub(d.blink).Seconds(); blinkAge >= 0 {
		if blinkAge >= .2 {
			d.blink = now.Add(time.Duration(2300+rand.IntN(2900)) * time.Millisecond)
		} else {
			pose.values[5] = max(pose.values[5], float32(math.Sin(blinkAge*math.Pi/.2)))
		}
	}
	return pose
}

func abs(x float32) float32 { return max(x, -x) }
