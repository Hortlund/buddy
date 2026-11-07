// Package references defines portable, read-only links into a Buddy home.
package references

import (
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

type Target struct {
	Kind string
	ID   string
	Path string
	Job  string
	Task string
}

func File(bud, relative string, task ...string) string {
	// Percent-encode spaces for native URL parsers as well as Go's form parser.
	values := url.Values{"path": {relative}}
	if len(task) != 0 && task[0] != "" {
		values.Set("task", task[0])
	}
	query := strings.ReplaceAll(values.Encode(), "+", "%20")
	u := url.URL{Scheme: "buddy", Host: "files", Path: "/" + bud, RawQuery: query}
	return u.String()
}

func Work(chat, job string) string {
	u := url.URL{Scheme: "buddy", Host: "work", Path: "/" + chat, RawQuery: url.Values{"job": {job}}.Encode()}
	return u.String()
}

func Parse(raw string) (Target, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "buddy" || u.User != nil || u.Opaque != "" || u.Fragment != "" {
		return Target{}, false
	}
	id := strings.TrimPrefix(u.Path, "/")
	if !validID(id) {
		return Target{}, false
	}
	t := Target{Kind: u.Host, ID: id}
	switch t.Kind {
	case "artifacts":
		t.Job = u.Query().Get("file")
		if !validID(t.Job) {
			return Target{}, false
		}
	case "files":
		t.Path = u.Query().Get("path")
		t.Task = u.Query().Get("task")
		if u.Query().Has("task") && !validID(t.Task) {
			return Target{}, false
		}
		if t.Path != "." && !ValidPath(t.Path) {
			return Target{}, false
		}
	case "work":
		t.Job = u.Query().Get("job")
		if !validID(t.Job) {
			return Target{}, false
		}
	case "chat":
	default:
		return Target{}, false
	}
	return t, true
}

func validID(id string) bool {
	if id == "" || len(id) > 256 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func ValidPath(relative string) bool {
	if len(relative) > 2048 || !fs.ValidPath(relative) || strings.ContainsAny(relative, "\\:") ||
		strings.IndexFunc(relative, unicode.IsControl) >= 0 {
		return false
	}
	for part := range strings.SplitSeq(relative, "/") {
		if strings.HasPrefix(part, ".") {
			return false
		}
	}
	return true
}

var inlineFile = regexp.MustCompile("`([^`\n]+)`")

// LinkFiles makes existing inline-code file references clickable.
// It leaves fenced code, existing links, and missing files alone.
// The viewer still validates access and symlinks when a link is followed.
func LinkFiles(text, bud, workspace string, task ...string) string {
	if workspace == "" || !validID(bud) || !strings.Contains(text, "`") {
		return text
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return text
	}
	defer root.Close()
	var output strings.Builder
	var fence byte
	var fenceSize int
	for line := range strings.SplitAfterSeq(text, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if len(trimmed) >= 3 && (trimmed[0] == '`' || trimmed[0] == '~') {
			n := 1
			for n < len(trimmed) && trimmed[n] == trimmed[0] {
				n++
			}
			if n >= 3 && fence == 0 {
				fence, fenceSize = trimmed[0], n
			} else if trimmed[0] == fence && n >= fenceSize && strings.TrimSpace(trimmed[n:]) == "" {
				fence = 0
			}
			output.WriteString(line)
			continue
		}
		if fence != 0 {
			output.WriteString(line)
			continue
		}
		cursor := 0
		for _, match := range inlineFile.FindAllStringSubmatchIndex(line, -1) {
			start, end := match[0], match[1]
			output.WriteString(line[cursor:start])
			code := line[start:end]
			relative := strings.TrimSuffix(line[match[2]:match[3]], "/")
			if filepath.IsAbs(relative) {
				relative, err = filepath.Rel(workspace, relative)
				if err != nil {
					relative = ""
				}
				relative = filepath.ToSlash(relative)
			} else {
				relative = strings.TrimPrefix(relative, "./")
			}
			// Don't nest a link or reinterpret a double-backtick code span.
			skip := strings.Contains(line[:start], "](") || strings.Count(line[:start], "[") > strings.Count(line[:start], "]") ||
				start > 0 && line[start-1] == '`' || end < len(line) && line[end] == '`'
			if !skip && ValidPath(relative) {
				if info, err := root.Lstat(relative); err == nil && (info.IsDir() || info.Mode().IsRegular()) {
					code = "[" + code + "](" + File(bud, path.Clean(relative), task...) + ")"
				}
			}
			output.WriteString(code)
			cursor = end
		}
		output.WriteString(line[cursor:])
	}
	return output.String()
}
