package voiceaudio

import (
	"encoding/binary"
	"errors"
	"math"
	"sync"
)

var ErrDevice = errors.New("Couldn’t start microphone and speaker audio. Check your sound devices and try again.")

// Level estimates the mic signal for the call meter. PCM never needs to be
// retained by the interface.
func Level(pcm []byte) float32 {
	var energy float64
	for i := 0; i+1 < len(pcm); i += 2 {
		value := float64(int16(binary.LittleEndian.Uint16(pcm[i:]))) / 32768
		energy += value * value
	}
	if len(pcm) < 2 {
		return 0
	}
	rms := math.Sqrt(energy / float64(len(pcm)/2))
	return float32(min(1, max(0, (20*math.Log10(rms)+60)/46)))
}

var ringWave = sync.OnceValue(func() []byte {
	const rate, frames = 24000, 24000 * 2
	wave := make([]byte, 44+frames*2)
	copy(wave, "RIFF")
	binary.LittleEndian.PutUint32(wave[4:], uint32(len(wave)-8))
	copy(wave[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(wave[16:], 16)
	binary.LittleEndian.PutUint16(wave[20:], 1) // PCM
	binary.LittleEndian.PutUint16(wave[22:], 1) // mono
	binary.LittleEndian.PutUint32(wave[24:], rate)
	binary.LittleEndian.PutUint32(wave[28:], rate*2)
	binary.LittleEndian.PutUint16(wave[32:], 2)
	binary.LittleEndian.PutUint16(wave[34:], 16)
	copy(wave[36:], "data")
	binary.LittleEndian.PutUint32(wave[40:], frames*2)
	for i := range frames {
		t := float64(i) / rate
		for j, note := range []float64{440, 587.33} {
			at := t - float64(j)*.23
			if at < 0 || at >= .21 {
				continue
			}
			envelope := min(1, at/.025) * math.Pow(1-at/.21, 1.5)
			sample := math.Sin(2*math.Pi*note*at) + .18*math.Sin(4*math.Pi*note*at)
			binary.LittleEndian.PutUint16(wave[44+2*i:], uint16(int16(3200*envelope*sample)))
		}
	}
	return wave
})
