package tasks

import (
	"bytes"
	"context"
	"database/sql"
	"image"
	_ "image/png"
)

// Avatar is part of the saved agent profile. A nil Avatar in a profile
// update leaves the saved appearance intact for older clients.
type Avatar struct {
	Color   string `json:"color"`
	Shape   string `json:"shape,omitempty"`
	Face    string `json:"face,omitempty"`   // Legacy client setting.
	Finish  string `json:"finish,omitempty"` // Legacy client setting.
	Animate bool   `json:"animate"`
	Image   []byte `json:"image,omitempty"`
}

const MaxAvatarImage = 128 << 10

func DefaultAvatar() Avatar {
	return Avatar{Color: "ink", Shape: "orb", Face: "smile", Finish: "satin", Animate: true}
}

func validateAvatar(a Avatar) error {
	switch a.Color {
	case "ink", "pearl", "lilac", "peach", "mint", "sky", "rose", "coral", "honey", "moss", "teal", "cobalt", "plum", "slate":
	default:
		return &InputError{"Choose one of the available avatar colors."}
	}
	switch a.Shape {
	case "", "orb", "bean", "pebble", "drop", "cloud", "capsule", "pillow":
	default:
		return &InputError{"Choose one of the available avatar shapes."}
	}
	switch a.Face {
	case "", "smile", "curious", "sleepy", "playful":
	default:
		return &InputError{"Invalid legacy avatar expression."}
	}
	if a.Finish != "" && a.Finish != "matte" && a.Finish != "satin" {
		return &InputError{"Invalid legacy avatar finish."}
	}
	if len(a.Image) == 0 {
		return nil
	}
	if len(a.Image) > MaxAvatarImage {
		return &InputError{"The avatar image is too large."}
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(a.Image))
	if err != nil || format != "png" || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 160 || cfg.Height > 160 {
		return &InputError{"Choose a valid avatar image."}
	}
	if _, _, err := image.Decode(bytes.NewReader(a.Image)); err != nil {
		return &InputError{"Choose a valid avatar image."}
	}
	return nil
}

func migrateAvatarShape(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil || version >= 1 {
		return err
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE agent_appearance ADD COLUMN shape TEXT NOT NULL DEFAULT 'orb'; PRAGMA user_version=1`); err != nil {
		return err
	}
	return tx.Commit()
}
