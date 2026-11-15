package tasks

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestExistingCustomSkillSurvivesNewBuiltIn(t *testing.T) {
	for _, name := range []string{"code", "inbox"} {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			path := filepath.Join(t.TempDir(), "buddy.db")
			before, err := OpenStore(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			// This row could be installed before the command became a built-in.
			_, err = before.db.ExecContext(ctx, "INSERT INTO installed_skills VALUES (?,?,?)", name, "My existing workflow", "Original custom instructions.")
			closeErr := before.Close()
			if err != nil || closeErr != nil {
				t.Fatalf("legacy home setup: %v, close: %v", err, closeErr)
			}
			store, err := OpenStore(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()

			checkListing := func(description string, builtIn bool) {
				t.Helper()
				skills, err := store.Skills(ctx)
				if err != nil {
					t.Fatal(err)
				}
				count := 0
				for _, skill := range skills {
					if skill.Name != name {
						continue
					}
					count++
					if skill.Description != description || skill.BuiltIn != builtIn {
						t.Errorf("wrong active definition: %+v", skill)
					}
				}
				if count != 1 {
					t.Errorf("got %d definitions for /%s, want one", count, name)
				}
			}
			checkListing("My existing workflow", false)
			first, _, err := store.Send(ctx, DefaultConversationID, "/"+name+" help me", "first")
			if err != nil {
				t.Fatal(err)
			}
			markdown := "---\nname: " + name + "\ndescription: Updated workflow\n---\nUpdated custom instructions."
			installed, err := store.InstallSkill(ctx, markdown)
			if err != nil || installed.BuiltIn || installed.Name != name {
				t.Fatalf("updating the existing custom skill: %+v, %v", installed, err)
			}
			checkListing("Updated workflow", false)
			turn, err := store.turn(ctx, first)
			if err != nil || turn.Skill == nil || turn.Skill.Instructions != "Original custom instructions." {
				t.Fatalf("queued command lost its custom definition: %+v, %v", turn.Skill, err)
			}
			if _, err := store.claim(ctx); err != nil {
				t.Fatal(err)
			}
			if err := store.finishTurn(ctx, first.ID, Succeeded, "Replied.", "Done.", &TurnAction{}); err != nil {
				t.Fatal(err)
			}
			second, _, err := store.Send(ctx, DefaultConversationID, "/"+name+" help again", "second")
			if err != nil {
				t.Fatal(err)
			}
			turn, err = store.turn(ctx, second)
			if err != nil || turn.Skill == nil || turn.Skill.Instructions != "Updated custom instructions." {
				t.Fatalf("new command did not use the updated definition: %+v, %v", turn.Skill, err)
			}
			if err := store.RemoveSkill(ctx, name); err != nil {
				t.Fatal(err)
			}
			var bundled skillDefinition
			for _, skill := range builtInSkills() {
				if skill.Name == name {
					bundled = skill
				}
			}
			checkListing(bundled.Description, true)
			instructions, err := skillInstructions(ctx, store.db, name)
			if err != nil || instructions != bundled.Instructions || strings.TrimSpace(instructions) == "" {
				t.Fatalf("bundled definition was not restored: %q, %v", instructions, err)
			}
			if _, err := store.InstallSkill(ctx, markdown); err == nil {
				t.Fatal("a fresh import replaced a reserved built-in")
			}
			if err := store.RemoveSkill(ctx, name); !errors.Is(err, ErrNotFound) {
				t.Fatalf("removed a built-in without a custom definition: %v", err)
			}
		})
	}
}
