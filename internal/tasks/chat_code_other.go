//go:build !linux

package tasks

import "context"

func PrepareChatCodeSandbox() error { return nil }

func nativeChatCodeRuntimes() map[string]chatCodeRuntime { return nil }

func runNativeChatCode(_ context.Context, _ string, toolchain chatCodeRuntime, _ string, _ func(chatCodeResult)) chatCodeResult {
	return chatCodeResult{Command: toolchain.commandLine(), Error: "Native code execution is unavailable."}
}
