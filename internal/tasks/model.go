package tasks

import "strings"

type Model struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func validModel(id string) bool {
	return id != "" && len(id) <= 128 && strings.IndexFunc(id, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-._:/", r))
	}) < 0
}
