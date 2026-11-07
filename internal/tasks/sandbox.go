package tasks

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"unicode/utf8"
)

func protectedRoots(paths []string) ([]string, error) {
	var roots []string
	for _, path := range paths {
		root, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil, err
		}
		root, err = filepath.Abs(root)
		if err != nil {
			return nil, err
		}
		if !utf8.ValidString(root) {
			return nil, fmt.Errorf("Buddy runtime path is not valid UTF-8: %q", root)
		}
		// Codex interprets these characters as globs after TOML decoding.
		// A quoted path containing them would not necessarily protect itself.
		if strings.ContainsAny(root, "*?[") {
			return nil, fmt.Errorf("Buddy runtime path contains sandbox glob characters: %q", root)
		}
		if !slices.Contains(roots, root) {
			roots = append(roots, root)
		}
	}
	return roots, nil
}

// A previously created hard link could expose the same inode under a writable
// path. Check the real service artifacts before handing control to the agent.
func validateProtectedRoots(roots []string, executionFiles bool, scratch ...string) error {
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if slices.Contains(scratch, path) {
					return filepath.SkipDir
				}
				// Local Git clones can share object storage. Buddy's launcher
				// builds the working tree and does not execute these objects.
				if executionFiles && entry.Name() == "objects" && filepath.Base(filepath.Dir(path)) == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			if executionFiles && entry.Type()&os.ModeSymlink != 0 {
				target, err := filepath.EvalSymlinks(path)
				if err != nil {
					return err
				}
				rel, err := filepath.Rel(root, target)
				if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
					return fmt.Errorf("Buddy runtime symlink leaves its trusted directory: %q", path)
				}
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || !info.Mode().IsRegular() || stat.Nlink != 1 {
				return fmt.Errorf("unsafe Buddy runtime file %q", path)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func sandboxArgs(workspace string, allowEdits, device bool, readOnly, protected []string, attachments ...string) []string {
	profile := ":read-only"
	if allowEdits {
		profile = ":workspace"
	}
	// Pass a whole TOML table, since the CLI splits dotted override keys even
	// inside quoted path components. JSON string quoting is compatible with
	// TOML basic strings for filesystem paths.
	var rules []string
	name := "buddy-runtime"
	if device {
		name = "buddy-device"
		access := "read"
		if allowEdits {
			access = "write"
		}
		rules = append(rules, `":root"="deny"`, `":minimal"="read"`, `":tmpdir"="deny"`, `":slash_tmp"="deny"`,
			`":workspace_roots"={"."=`+tomlString(access)+`}`)
		readOnly = nil
	}
	for _, path := range protected {
		if path != "" {
			rules = append(rules, tomlString(path)+`="deny"`)
		}
	}
	for _, path := range append(slices.Clone(readOnly), attachments...) {
		if path != "" && !slices.Contains(protected, path) {
			rules = append(rules, tomlString(path)+`="read"`)
		}
	}
	filesystem := "{" + strings.Join(rules, ",") + "}"
	return []string{
		"--strict-config",
		"-c", "default_permissions=" + tomlString(name),
		"-c", "permissions." + name + ".extends=" + tomlString(profile),
		"-c", "permissions." + name + ".filesystem=" + filesystem,
		"-c", "projects={" + tomlString(workspace) + `={trust_level="untrusted"}}`,
	}
}

func tomlString(s string) string {
	data, _ := json.Marshal(s)
	return strings.ReplaceAll(string(data), "\x7f", `\u007f`)
}

// An npm shim loads its platform binary from the installation's node_modules
// tree. Other runner binaries are installed alongside their launcher.
func runnerInstallDir(path string) string {
	separator := string(filepath.Separator)
	if i := strings.Index(path, separator+"node_modules"+separator); i >= 0 {
		return path[:i+len(separator+"node_modules")]
	}
	return filepath.Dir(path)
}

func checkWorkspace(dir string) (string, error) {
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory", dir)
	}
	return dir, nil
}

// WorkspacePathsOverlap compares directory identities as well as ancestry so aliases on
// case-insensitive filesystems cannot widen the editable workspace.
func WorkspacePathsOverlap(first, second string) (bool, error) {
	contains := func(root, dir string) (bool, error) {
		target, err := os.Stat(root)
		if err != nil {
			return false, err
		}
		for {
			current, err := os.Stat(dir)
			if err != nil {
				return false, err
			}
			if os.SameFile(target, current) {
				return true, nil
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				return false, nil
			}
			dir = parent
		}
	}
	inside, err := contains(first, second)
	if err != nil || inside {
		return inside, err
	}
	return contains(second, first)
}
