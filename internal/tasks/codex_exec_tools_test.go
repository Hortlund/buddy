package tasks

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"buddy/internal/localconfig"
)

func TestKeyringExecCanReadRoutineConnection(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("Python is needed for the Codex exec fixture")
	}
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	t.Setenv("CODEX_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	path := filepath.Join(dir, "codex-fixture")
	// Exercise the full login fallback with a local exec fixture. The real
	// Codex authentication provider is independent of this transport.
	const fixture = `#!/usr/bin/env python3
import json, os, re, sys, urllib.request
args = sys.argv[1:]
assert 'exec' in args and '--ignore-user-config' in args
assert 'cli_auth_credentials_store="auto"' in args
assert 'web_search="disabled"' in args
assert any(arg.startswith('shell_environment_policy.exclude=[') and '"BUDDY_EXEC_TOOL_BEARER"' in arg for arg in args)
config = next(a for a in args if a.startswith('mcp_servers='))
url = re.search(r'http://127\.0\.0\.1:\d+', config)[0]
def rpc(method, params, authenticated=True):
    headers = {'Content-Type':'application/json', 'Accept':'application/json, text/event-stream'}
    if authenticated: headers['Authorization'] = 'Bearer '+os.environ['BUDDY_EXEC_TOOL_BEARER']
    frame = json.dumps({'jsonrpc':'2.0','id':1,'method':method,'params':params}).encode()
    request = urllib.request.Request(url, frame, headers)
    with urllib.request.urlopen(request, timeout=5) as result:
        return json.load(result)['result']
try:
    rpc('tools/list', {}, False)
    raise AssertionError('unauthenticated lookup succeeded')
except urllib.error.HTTPError as error:
    assert error.code == 401
tools = rpc('tools/list', {})['tools']
assert any(x['name']=='connection_read' for x in tools)
result = rpc('tools/call', {'name':'connection_read','arguments':{'source':'gmail_label','kind':'search','query':'in:inbox'}})
assert not result['isError']
heading = json.loads(result['content'][0]['text'])['heading']
answer = json.dumps({'reply':heading,'needs_input':False})
print(json.dumps({'type':'item.completed','item':{'type':'agent_message','text':answer}}))
`
	if err := os.WriteFile(path, []byte(fixture), 0700); err != nil {
		t.Fatal(err)
	}
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runtime := CodexRuntime{path: path, Key: localconfig.CodexKey{Dir: dir}}
	turn := Turn{Origin: "routine", Brief: "Read inbox", CanRequestInput: true, integrations: testChatIntegrations{},
		connections: []ProviderConnection{{ID: "mail", Name: "Mail", Source: GmailLabel, Connected: true}}}
	reply, err := runtime.runPrompt(t.Context(), workspace, turn, nil, nil, nil, "", func(string, string) error { return nil }, "Read inbox.")
	if err != nil || reply != "Email preview" {
		t.Fatalf("routine reply = %q, %v", reply, err)
	}
}

func TestPublicSearchRejectsModelSuppliedQueries(t *testing.T) {
	var calls, corrections []string
	turn := Turn{chatTools: &chatToolState{}, publicRequest: "Search Go release notes.",
		steering: &turnSteering{applied: func(context.Context) ([]string, error) { return corrections, nil }}}
	turn.searchPublic = turn.publicSearch(func(_ context.Context, scope publicResearchContext) (string, error) {
		if scope.Requests[0].User != "Search Go release notes." {
			t.Fatalf("missing authenticated request: %+v", scope.Requests)
		}
		if scope.Requests[len(scope.Requests)-1].User == "Cancel the search." {
			return "", &InputError{"No current search requested."}
		}
		return scope.Requests[len(scope.Requests)-1].User, nil
	}, func(_ context.Context, request string) (string, error) {
		calls = append(calls, request)
		return "Public results for " + request, nil
	})
	_, handlers := turn.toolsForChat(t.Context(), "", nil, func(string, string) error { return nil })
	tool := handlers["buddy.search_public"]
	if result := tool.call(json.RawMessage(`{"query":"private record"}`)); result.success || len(calls) != 0 {
		t.Fatalf("accepted model-supplied query: %+v", result)
	}
	for range 2 {
		if result := tool.call(json.RawMessage(`{}`)); !result.success || !strings.Contains(result.text, "Public results") {
			t.Fatalf("search failed: %+v", result)
		}
	}
	if len(calls) != 1 || calls[0] != "Search Go release notes." {
		t.Fatalf("expected one search of the original request: %v", calls)
	}
	corrections = append(corrections, "Search Rust release notes instead.")
	if result := tool.call(json.RawMessage(`{}`)); !result.success || !strings.Contains(result.text, "Rust release notes") || len(calls) != 2 {
		t.Fatalf("ignored live correction: %+v, calls=%v", result, calls)
	}
	corrections = append(corrections, "Cancel the search.")
	if result := tool.call(json.RawMessage(`{}`)); result.success || len(calls) != 2 {
		t.Fatalf("ignored cancellation: %+v, calls=%v", result, calls)
	}
}

func codexExecFixture(t *testing.T, script string) *CodexRuntime {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	t.Setenv("CODEX_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	path := filepath.Join(dir, "codex-fixture")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return &CodexRuntime{path: path, Key: localconfig.CodexKey{Dir: dir}}
}

func TestKeyringExplainsRecordedRunInIsolatedSession(t *testing.T) {
	runtime := codexExecFixture(t, `#!/bin/sh
set -eu
case "$*" in
  *--ignore-user-config*) ;;
  *) exit 1 ;;
esac
case "$*" in
  *features.shell_tool=false*web_search=*disabled*) ;;
  *) exit 1 ;;
esac
case "$*" in
  *mcp_servers*) exit 1 ;;
esac
request=$(cat)
case "$request" in
  *"Why did it fail?"*"fixture compile error"*) ;;
  *) exit 1 ;;
esac
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"It failed while compiling the fixture."}}'
`)
	reply, err := runtime.explainRecordedCodeResult(t.Context(), "fixture", "en", "Why did it fail?", ChatCodeRun{
		Status: Failed, chatCodeResult: chatCodeResult{Stderr: "fixture compile error"},
	})
	if err != nil || reply != "It failed while compiling the fixture." {
		t.Fatalf("keyring explanation = %q, %v", reply, err)
	}
}

func TestKeyringPublishesBackgroundCommandCards(t *testing.T) {
	runtime := codexExecFixture(t, `#!/bin/sh
cat >/dev/null
printf '%s\n' \
  '{"type":"item.started","item":{"type":"command_execution","id":"item_1","command":"go test ./...","status":"in_progress","aggregated_output":"","exit_code":null}}' \
  '{"type":"item.completed","item":{"type":"command_execution","id":"item_1","command":"go test ./...","status":"failed","aggregated_output":"Running tests\nFAIL\n","exit_code":1}}' \
  '{"type":"item.completed","item":{"type":"agent_message","text":"{\"reply\":\"Test run failed.\",\"needs_input\":false}"}}'
`)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var runs []ChatCodeRun
	turn := Turn{Brief: "Run tests", CanRequestInput: true}
	reply, err := runtime.runPrompt(t.Context(), dir, turn, nil, nil, nil, "", func(stream, text string) error {
		if stream == chatCodeStream {
			var run ChatCodeRun
			if err := json.Unmarshal([]byte(text), &run); err != nil {
				return err
			}
			runs = append(runs, run)
		}
		return nil
	}, turn.Brief)
	if err != nil || reply != "Test run failed." || len(runs) != 2 {
		t.Fatalf("command snapshots = %+v, reply %q, %v", runs, reply, err)
	}
	if runs[0].Status != Running || runs[0].Command != "go test ./..." || runs[1].Status != Failed ||
		runs[1].Stdout != "Running tests\nFAIL\n" || runs[1].ExitCode == nil || *runs[1].ExitCode != 1 {
		t.Fatalf("incorrect command snapshots: %+v", runs)
	}
}
