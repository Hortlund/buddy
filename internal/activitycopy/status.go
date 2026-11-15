// Package activitycopy supplies the short labels shown while a Bud is active.
package activitycopy

import (
	_ "embed"
	"encoding/json"
	"hash/fnv"
)

const IntervalSeconds = 5

//go:embed status.json
var catalog []byte

var labels = func() map[string][]string {
	var copy map[string][]string
	if err := json.Unmarshal(catalog, &copy); err != nil {
		panic(err)
	}
	return copy
}()

// Label gives each run and stage a different ordering and visits the entire
// stage's catalog before repeating. The same order is used on iPhone.
func Label(run, stage string, step int) string {
	if len(labels[stage]) == 0 {
		stage = "thinking"
	}
	pool := labels[stage]
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(run + ":" + stage))
	value, n := int(uint64(hash.Sum32())), len(pool)
	stride := value/n%(n-1) + 1
	for gcd(stride, n) != 1 {
		stride++
	}
	return pool[(value%n+step%n*stride)%n]
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}
