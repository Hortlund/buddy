package tasks

import (
	"slices"
	"strings"
)

type Model struct {
	ID            string       `json:"id"`
	Name          string       `json:"name"`
	Efforts       []string     `json:"efforts,omitempty"`
	DefaultEffort string       `json:"default_effort,omitempty"`
	Speeds        []ModelSpeed `json:"speeds,omitempty"`
}

type ModelSpeed struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Preferences are captured with the turn so changing a Bud cannot change queued work.
type ModelPreferences struct {
	Mode     string             `json:"mode,omitempty"`
	Effort   string             `json:"effort,omitempty"`
	Speed    string             `json:"speed,omitempty"`
	Profiles []TaskModelProfile `json:"profiles,omitempty"`
	Selected string             `json:"selected_profile,omitempty"`
}

type TaskModelProfile struct {
	Kind   string `json:"kind"`
	Model  string `json:"model"`
	Effort string `json:"effort,omitempty"`
	Speed  string `json:"speed,omitempty"`
}

var TaskProfileKinds = []string{"coding", "review", "research", "mail", "everyday"}

func (p *ModelPreferences) validate() error {
	if p == nil {
		return nil
	}
	if (p.Mode != "" && p.Mode != "auto" && p.Mode != "fixed") || !validModelOption(p.Effort) || !validModelOption(p.Speed) || len(p.Profiles) > len(TaskProfileKinds) {
		return &InputError{"Choose valid effort and speed options, with at most five task profiles."}
	}
	seen := make(map[string]bool)
	for _, profile := range p.Profiles {
		if !slices.Contains(TaskProfileKinds, profile.Kind) || seen[profile.Kind] || (profile.Model != "" && !validModel(profile.Model)) ||
			!validModelOption(profile.Effort) || !validModelOption(profile.Speed) {
			return &InputError{"Each task profile needs a unique task type, a Codex model ID, and valid effort and speed options."}
		}
		seen[profile.Kind] = true
	}
	return nil
}

func validModelOption(option string) bool {
	return option == "" || len(option) <= 64 && strings.IndexFunc(option, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-')
	}) < 0
}

func (p *ModelPreferences) Clone() *ModelPreferences {
	if p == nil {
		return nil
	}
	copy := *p
	copy.Profiles = append([]TaskModelProfile(nil), p.Profiles...)
	return &copy
}

func validModel(id string) bool {
	return id != "" && len(id) <= 128 && strings.IndexFunc(id, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-._:/", r))
	}) < 0
}

func (p *ModelPreferences) configured() bool {
	if p == nil {
		return false
	}
	if p.Mode != "" {
		return true
	}
	if p.Effort != "" || p.Speed != "" {
		return true
	}
	for _, profile := range p.Profiles {
		if profile.Model != "" {
			return true
		}
	}
	return false
}
