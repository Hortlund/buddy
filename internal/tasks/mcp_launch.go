package tasks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
)

type mcpLaunch struct {
	command     string
	args        []string
	roots       []string
	fingerprint string
}

// Bind owner approval to the canonical executable, explicit paths and file
// contents. A missing file or a newly resolved target needs a fresh save.
func inspectMCPLaunch(ctx context.Context, command string, args []string) (mcpLaunch, error) {
	launcher, err := exec.LookPath(command)
	if err != nil {
		return mcpLaunch{}, &InputError{"The MCP executable wasn't found on the Buddy server. Check its path and permissions."}
	}
	executable, err := filepath.EvalSymlinks(launcher)
	if err != nil || !filepath.IsAbs(executable) {
		return mcpLaunch{}, &InputError{"Use an executable installed at an absolute path on the Buddy server."}
	}

	launch := mcpLaunch{command: executable, args: slices.Clone(args)}
	paths := []string{executable, runnerInstallDir(executable), filepath.Dir(launcher)}
	files := []string{executable}
	for i, arg := range args {
		path, prefix := arg, ""
		if strings.HasPrefix(arg, "-") {
			flag, value, _ := strings.Cut(arg, "=")
			path, prefix = value, flag+"="
		}
		if !filepath.IsAbs(path) {
			continue
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return mcpLaunch{}, &InputError{"Every absolute MCP argument path must already exist on the Buddy server."}
		}
		info, err := os.Stat(resolved)
		if err != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
			return mcpLaunch{}, &InputError{"Use an existing file or directory for each absolute MCP argument path."}
		}
		launch.args[i] = prefix + resolved
		if info.Mode().IsRegular() {
			files = append(files, resolved)
			paths = append(paths, resolved, filepath.Dir(path), filepath.Dir(resolved))
		}
	}
	launch.roots, err = protectedRoots(paths)
	if err != nil || slices.Contains(launch.roots, string(filepath.Separator)) {
		return mcpLaunch{}, &InputError{"Choose an MCP installation outside the filesystem root, without sandbox glob characters."}
	}

	hash := sha256.New()
	encoder := json.NewEncoder(hash)
	// These values are strings only and the hash writer never returns an error.
	_ = encoder.Encode(launch.args)
	for _, path := range files {
		digest, err := mcpFileDigest(ctx, path)
		if err != nil {
			return mcpLaunch{}, err
		}
		_ = encoder.Encode([2]string{path, digest})
	}
	launch.fingerprint = hex.EncodeToString(hash.Sum(nil))
	return launch, nil
}

func mcpFileDigest(ctx context.Context, path string) (string, error) {
	unsafe := &InputError{"Use regular MCP launch files without writable hard links. Reinstall or copy the file to a trusted location."}
	info, err := os.Lstat(path)
	if err != nil || !safeMCPLaunchFile(path, info) {
		return "", unsafe
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return "", unsafe
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !safeMCPLaunchFile(path, opened) {
		return "", unsafe
	}
	hash := sha256.New()
	buf := make([]byte, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := file.Read(buf)
		_, _ = hash.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", unsafe
		}
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(opened, after) || !safeMCPLaunchFile(path, after) ||
		opened.Size() != after.Size() || !opened.ModTime().Equal(after.ModTime()) {
		return "", unsafe
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func safeMCPLaunchFile(path string, info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() {
		return false
	}
	if stat.Nlink == 1 {
		return true
	}
	// Allow the hard-linked executables that ship on macOS's sealed system
	// volume. A general read-only mount can still have a writable bind alias.
	dir := filepath.Dir(path)
	if runtime.GOOS != "darwin" || stat.Nlink < 2 ||
		(dir != "/bin" && dir != "/sbin" && dir != "/usr/bin" && dir != "/usr/sbin") {
		return false
	}
	const readOnlyMount = 1
	var fs syscall.Statfs_t
	return syscall.Statfs(path, &fs) == nil && fs.Flags&readOnlyMount != 0
}

func (server *MCPServerConfig) verifyMCPLaunch(ctx context.Context) ([]string, error) {
	if server.launchFingerprint == "" {
		return nil, &InputError{"Review this local command and save it again before Buddy can launch it."}
	}
	launch, err := inspectMCPLaunch(ctx, server.Command, server.Args)
	if err != nil {
		return nil, err
	}
	if launch.fingerprint != server.launchFingerprint {
		return nil, &InputError{"The MCP launch files changed. Review the command and save it again to approve the current files."}
	}
	server.Command, server.Args = launch.command, launch.args
	return launch.roots, nil
}
