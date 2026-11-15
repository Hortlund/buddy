package tasks

import (
	"encoding/json"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestSnippetSchemaAdvertisesOnlyDetectedLanguages(t *testing.T) {
	var schema struct {
		Properties struct{ Language struct{ Enum []string } }
	}
	if err := json.Unmarshal(chatToolInputSchema(map[string]chatCodeRuntime{"typescript": {}, "sql": {}, "bash": {}}), &schema); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(schema.Properties.Language.Enum, []string{"bash", "sql", "typescript"}) {
		t.Fatalf("incorrect language catalog: %v", schema.Properties.Language.Enum)
	}
}

func TestAdditionalSnippetLanguagesExecuteInSandbox(t *testing.T) {
	runtimes := chatCodeRuntimes()
	sandbox, _ := exec.LookPath("codex")
	for _, fixture := range []struct{ language, source, want string }{
		{"javascript", `import { strict as assert } from 'node:assert'; assert.equal(6*7,42); console.log(42);`, "42\n"},
		{"typescript", `const values: number[] = [20,22]; console.log(values.reduce((a,b)=>a+b,0));`, "42\n"},
		{"ruby", `puts [20,22].sum`, "42\n"},
		{"bash", `printf '%s\n' "$((20+22))"`, "42\n"},
		{"sql", `CREATE TABLE items(value INTEGER); INSERT INTO items VALUES(20),(22); SELECT SUM(value) AS total FROM items;`, "[42]\n"},
	} {
		t.Run(fixture.language, func(t *testing.T) {
			runtime, ok := runtimes[fixture.language]
			if !ok {
				t.Skip("isolated runtime unavailable")
			}
			result := runChatCode(t.Context(), sandbox, runtime, fixture.source, nil)
			if result.Error != "" || result.ExitCode == nil || *result.ExitCode != 0 || !strings.Contains(result.Stdout, fixture.want) {
				t.Fatalf("execution failed: %+v", result)
			}
		})
	}
}

func TestSQLSnippetRespectsTransactionControl(t *testing.T) {
	runtime, ok := chatCodeRuntimes()["sql"]
	if !ok {
		t.Skip("isolated SQLite runtime unavailable")
	}
	sandbox, _ := exec.LookPath("codex")
	t.Run("explicit commit and rollback after an earlier write", func(t *testing.T) {
		result := runChatCode(t.Context(), sandbox, runtime, `
CREATE TABLE items(value INTEGER);
INSERT INTO items VALUES(1);
BEGIN;
INSERT INTO items VALUES(2);
COMMIT;
BEGIN;
INSERT INTO items VALUES(99);
ROLLBACK;
SELECT value FROM items ORDER BY value;
`, nil)
		if result.Error != "" || result.ExitCode == nil || *result.ExitCode != 0 || !strings.HasSuffix(result.Stdout, "[1]\n[2]\n") {
			t.Fatalf("transaction script failed: %+v", result)
		}
	})
	t.Run("foreign keys can be enabled after an earlier write", func(t *testing.T) {
		result := runChatCode(t.Context(), sandbox, runtime, `
CREATE TABLE parents(id INTEGER PRIMARY KEY);
CREATE TABLE children(parent_id INTEGER REFERENCES parents(id));
INSERT INTO parents VALUES(1);
PRAGMA foreign_keys=ON;
INSERT INTO children VALUES(42);
`, nil)
		if result.ExitCode == nil || *result.ExitCode == 0 || !strings.Contains(result.Stderr, "FOREIGN KEY constraint failed") {
			t.Fatalf("SQL runner accepted an invalid foreign key: %+v", result)
		}
	})
}
