package main

import (
	"strings"

	"github.com/egoist/mygo/ui/editor"
)

// The editor presents LF to callers. Retain the previous endings for the
// corresponding lines and use the neighboring style for inserted lines.
func workspaceEditedText(original, edited string) string {
	crlf := strings.Count(original, "\r\n")
	switch {
	case crlf == 0 || edited == "":
		return edited
	case crlf == strings.Count(original, "\n"):
		return strings.ReplaceAll(edited, "\n", "\r\n")
	}
	oldLines := strings.SplitAfter(original, "\n")
	newLines := strings.SplitAfter(edited, "\n")
	diff := editor.DiffState{Old: editor.NewBuffer(original), New: editor.NewBuffer(edited)}
	var text strings.Builder
	text.Grow(len(edited) + crlf)
	writeLine := func(line string, oldIndex int) {
		if strings.HasSuffix(line, "\n") && strings.HasSuffix(oldLines[oldIndex], "\r\n") {
			text.WriteString(strings.TrimSuffix(line, "\n"))
			text.WriteString("\r\n")
		} else {
			text.WriteString(line)
		}
	}
	oldAt, newAt := 0, 0
	for _, hunk := range diff.Hunks() {
		for i, line := range newLines[newAt:hunk.NewStart] {
			writeLine(line, oldAt+i)
		}
		for i, line := range newLines[hunk.NewStart:hunk.NewEnd] {
			neighbor := max(0, min(hunk.OldStart+i, hunk.OldEnd-1))
			writeLine(line, neighbor)
		}
		oldAt, newAt = hunk.OldEnd, hunk.NewEnd
	}
	for i, line := range newLines[newAt:] {
		writeLine(line, oldAt+i)
	}
	return text.String()
}
