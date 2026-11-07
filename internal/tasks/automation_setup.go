package tasks

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"buddy/internal/localconfig"
)

// RunAutomationSetup runs only at the host owner's explicit request. It tests
// MCP discovery without a model turn and records the exact approved tool list.
func RunAutomationSetup(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("setup-automation", flag.ContinueOnError)
	flags.SetOutput(output)
	path := flags.String("config", "", "private execution.json path on this host")
	provider := flags.String("provider", "", "playwright or cua")
	codex := flags.String("codex", "codex", "Codex CLI executable")
	disable := flags.Bool("disable", false, "remove this provider from the host configuration")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *path == "" || (*provider != "playwright" && *provider != "cua") {
		return &InputError{"Provide -config /private/path/execution.json and -provider playwright or cua. This enables interaction tools on that host."}
	}
	absolute, err := filepath.Abs(*path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0700); err != nil {
		return err
	}
	dir, err := localconfig.PrivateDir(filepath.Dir(absolute))
	if err != nil {
		return err
	}
	cfg, err := LoadExecutionConfig(absolute)
	if err != nil {
		return err
	}
	runner, err := NewCodexRuntime(*codex, "", localconfig.CodexKey{Dir: dir}, dir)
	if err != nil {
		return err
	}
	cfg.Automation = slices.DeleteFunc(cfg.Automation, func(a AutomationSetup) bool { return a.Provider == *provider })
	if !*disable {
		setup, err := runner.TestAutomation(ctx, *provider)
		if err != nil {
			return err
		}
		cfg.Automation = append(cfg.Automation, setup)
	}
	if err := runner.ConfigureExecution(ctx, cfg); err != nil {
		return err
	}
	data, err := marshalExecutionConfig(runner.ExecutionConfig())
	if err != nil {
		return err
	}
	if err := writePrivateExecution(filepath.Join(dir, filepath.Base(absolute)), data); err != nil {
		return err
	}
	fmt.Fprintf(output, "Saved %s. Restart this host's Buddy server or worker to load the change.\n", absolute)
	return nil
}
