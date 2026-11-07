package tasks

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"regexp"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

const MaxSkillBytes = 32 << 10
const reviewSkillName = "review"

const skillSchema = `
CREATE TABLE IF NOT EXISTS installed_skills (
	name TEXT PRIMARY KEY, description TEXT NOT NULL, instructions TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS turn_skills (
	task_id TEXT PRIMARY KEY REFERENCES chat_turns(task_id),
	name TEXT NOT NULL, arguments TEXT NOT NULL, instructions TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS routine_skill_sources (
	routine_id TEXT PRIMARY KEY REFERENCES routines(id) ON DELETE CASCADE,
	task_id TEXT NOT NULL REFERENCES turn_skills(task_id)
);
CREATE TABLE IF NOT EXISTS bud_proposals (
	task_id TEXT PRIMARY KEY REFERENCES chat_turns(task_id), bud_id TEXT NOT NULL UNIQUE,
	name TEXT NOT NULL, role TEXT NOT NULL, instructions TEXT NOT NULL
);
`

type SkillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	BuiltIn     bool   `json:"built_in,omitempty"`
}

type skillDefinition struct {
	SkillInfo
	Instructions string
}

type SkillInvocation struct {
	Name         string `json:"name"`
	Arguments    string `json:"arguments"`
	Instructions string `json:"instructions"`
}

var skillName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

//go:embed skills/*/SKILL.md
var skillFiles embed.FS

var builtInSkills = sync.OnceValue(func() []skillDefinition {
	entries, err := skillFiles.ReadDir("skills")
	if err != nil {
		panic(err)
	}
	skills := make([]skillDefinition, 0, len(entries))
	for _, entry := range entries {
		data, err := skillFiles.ReadFile("skills/" + entry.Name() + "/SKILL.md")
		if err != nil {
			panic(err)
		}
		skill, err := parseSkill(string(data))
		if err != nil || skill.Name != entry.Name() {
			panic("invalid bundled Buddy skill: " + entry.Name())
		}
		skill.BuiltIn = true
		skills = append(skills, skill)
	}
	return skills
})

func parseSkill(markdown string) (skillDefinition, error) {
	if len(markdown) > MaxSkillBytes || !utf8.ValidString(markdown) || strings.ContainsRune(markdown, 0) {
		return skillDefinition{}, &InputError{"Choose a UTF-8 SKILL.md file up to 32 KB."}
	}
	markdown = strings.ReplaceAll(strings.TrimPrefix(markdown, "\uFEFF"), "\r\n", "\n")
	front, ok := strings.CutPrefix(markdown, "---\n")
	metadata, instructions, found := strings.Cut(front, "\n---\n")
	if !ok || !found || strings.TrimSpace(instructions) == "" {
		return skillDefinition{}, &InputError{"A skill needs YAML front matter followed by Markdown instructions."}
	}
	var header struct {
		Name          string `yaml:"name"`
		Description   string `yaml:"description"`
		UserInvocable *bool  `yaml:"user-invocable"`
	}
	if err := yaml.Unmarshal([]byte(metadata), &header); err != nil {
		return skillDefinition{}, &InputError{"The SKILL.md front matter is invalid YAML."}
	}
	description := strings.Join(strings.Fields(header.Description), " ")
	if !validSkillName(header.Name) || !validTitle(description, 1024) {
		return skillDefinition{}, &InputError{"Use a lowercase skill name with letters, digits, or hyphens and include a description."}
	}
	if header.UserInvocable != nil && !*header.UserInvocable {
		return skillDefinition{}, &InputError{"Buddy imports user-invocable skills. Set user-invocable to true to use this as a slash command."}
	}
	return skillDefinition{SkillInfo: SkillInfo{Name: header.Name, Description: description}, Instructions: strings.TrimSpace(instructions)}, nil
}

func validSkillName(name string) bool { return len(name) <= 64 && skillName.MatchString(name) }

func (s *Store) InstallSkill(ctx context.Context, markdown string) (SkillInfo, error) {
	skill, err := parseSkill(markdown)
	if err != nil {
		return SkillInfo{}, err
	}
	for _, builtIn := range builtInSkills() {
		if skill.Name == builtIn.Name {
			return SkillInfo{}, &InputError{"That name belongs to a built-in skill. Choose a different name."}
		}
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO installed_skills SELECT ?,?,? WHERE
		EXISTS(SELECT 1 FROM installed_skills WHERE name=?) OR (SELECT count(*) FROM installed_skills)<50
		ON CONFLICT(name) DO UPDATE SET description=excluded.description,instructions=excluded.instructions`,
		skill.Name, skill.Description, skill.Instructions, skill.Name)
	if err := requireStandingChange(result, err); errors.Is(err, ErrInactive) {
		return SkillInfo{}, &InputError{"Remove a custom skill before adding another. This Buddy home supports 50 custom skills."}
	} else if err != nil {
		return SkillInfo{}, err
	}
	return skill.SkillInfo, nil
}

func (s *Store) RemoveSkill(ctx context.Context, name string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM installed_skills WHERE name=?", name)
	if err := requireStandingChange(result, err); errors.Is(err, ErrInactive) {
		return ErrNotFound
	} else {
		return err
	}
}

func listSkills(ctx context.Context, db queryer) ([]SkillInfo, error) {
	var out []SkillInfo
	for _, skill := range builtInSkills() {
		out = append(out, skill.SkillInfo)
	}
	rows, err := db.QueryContext(ctx, "SELECT name,description FROM installed_skills ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var skill SkillInfo
		if err := rows.Scan(&skill.Name, &skill.Description); err != nil {
			return nil, err
		}
		out = append(out, skill)
	}
	return out, rows.Err()
}

func (s *Store) Skills(ctx context.Context) ([]SkillInfo, error) { return listSkills(ctx, s.db) }

// SkillCommand splits a leading slash command from its arguments.
func SkillCommand(text string) (name, arguments string) {
	command, ok := strings.CutPrefix(strings.TrimSpace(text), "/")
	if !ok {
		return "", ""
	}
	name = command
	if split := strings.IndexFunc(command, unicode.IsSpace); split >= 0 {
		name, arguments = command[:split], strings.TrimSpace(command[split:])
	}
	if !validSkillName(name) {
		return "", ""
	}
	return name, arguments
}

func skillInstructions(ctx context.Context, tx *sql.Tx, name string) (string, error) {
	for _, skill := range builtInSkills() {
		if skill.Name == name {
			return skill.Instructions, nil
		}
	}
	var instructions string
	err := tx.QueryRowContext(ctx, "SELECT instructions FROM installed_skills WHERE name=?", name).Scan(&instructions)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return instructions, err
}

func bindSkill(ctx context.Context, tx *sql.Tx, id, text, selected string) error {
	name, arguments := SkillCommand(text)
	if selected != "" && selected != name {
		return &InputError{"The selected skill must match the command at the start of your message."}
	}
	if name == "" {
		return nil
	}
	instructions, err := skillInstructions(ctx, tx, name)
	if err != nil {
		return err
	}
	if instructions == "" {
		if selected != "" {
			return &InputError{"The selected skill /" + name + " is no longer installed. Choose another skill or remove the slash command."}
		}
		return nil
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO turn_skills VALUES (?,?,?,?)", id, name, arguments, instructions)
	if err == nil && name == reviewSkillName {
		return constrainReviewTurn(ctx, tx, id)
	}
	return err
}

func continueSkill(ctx context.Context, tx *sql.Tx, id, original string) error {
	result, err := tx.ExecContext(ctx, `INSERT INTO turn_skills SELECT ?,name,arguments,instructions
		FROM turn_skills WHERE task_id=?`, id, original)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil || count == 0 {
		return err
	}
	return constrainReviewTurn(ctx, tx, id)
}

func constrainReviewTurn(ctx context.Context, tx *sql.Tx, id string) error {
	_, err := tx.ExecContext(ctx, `UPDATE chat_turns SET allow_edits=0 WHERE task_id=?
		AND EXISTS(SELECT 1 FROM turn_skills WHERE task_id=? AND name=?)`, id, id, reviewSkillName)
	return err
}

func skillForTurn(ctx context.Context, db queryer, id, parent string) (*SkillInvocation, error) {
	if parent != "" {
		id = parent
	}
	var skill SkillInvocation
	err := db.QueryRowContext(ctx, "SELECT name,arguments,instructions FROM turn_skills WHERE task_id=?", id).
		Scan(&skill.Name, &skill.Arguments, &skill.Instructions)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &skill, err
}
