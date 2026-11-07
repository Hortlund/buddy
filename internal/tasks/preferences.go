package tasks

import (
	"context"
	"strings"

	"golang.org/x/text/language"
	"golang.org/x/text/language/display"
)

const preferencesSchema = `
CREATE TABLE IF NOT EXISTS language_preferences (
	id INTEGER PRIMARY KEY CHECK (id=1),
	chat TEXT NOT NULL,
	voice TEXT NOT NULL
);
INSERT OR IGNORE INTO language_preferences VALUES (1,'','');
`

type LanguagePreferences struct {
	Chat  string `json:"chat"`
	Voice string `json:"voice"`
}

func LanguageName(code string) string {
	if code == "" {
		return "Automatic"
	}
	return display.English.Languages().Name(language.Make(code))
}

func validLanguage(code string) bool {
	if code == "" {
		return true
	}
	tag, err := language.Parse(code)
	base, _ := tag.Base()
	return len(code) == 2 && err == nil && base.String() == code && !base.IsPrivateUse() && code != "und"
}

func (s *Store) Languages(ctx context.Context) (LanguagePreferences, error) {
	var prefs LanguagePreferences
	err := s.db.QueryRowContext(ctx, "SELECT chat,voice FROM language_preferences WHERE id=1").Scan(&prefs.Chat, &prefs.Voice)
	return prefs, err
}

func (s *Store) SaveLanguages(ctx context.Context, prefs LanguagePreferences) (LanguagePreferences, error) {
	prefs.Chat = strings.ToLower(strings.TrimSpace(prefs.Chat))
	prefs.Voice = strings.ToLower(strings.TrimSpace(prefs.Voice))
	if !validLanguage(prefs.Chat) || !validLanguage(prefs.Voice) {
		return LanguagePreferences{}, &InputError{"Use a two-letter language code or choose Automatic."}
	}
	_, err := s.db.ExecContext(ctx, "UPDATE language_preferences SET chat=?,voice=? WHERE id=1", prefs.Chat, prefs.Voice)
	return prefs, err
}
