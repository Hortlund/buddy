package localconfig

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestUpgradeReplacesLegacyTokenAndKeepsIdentityAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	legacy, err := EnsureToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	first, err := EnsureIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	upgraded, err := ReadToken(filepath.Join(dir, TokenName))
	if err != nil || upgraded == legacy {
		t.Fatalf("legacy token wasn't rotated: %v", err)
	}
	reopened, err := EnsureIdentity(dir)
	if err != nil || !bytes.Equal(first.Certificate[0], reopened.Certificate[0]) {
		t.Fatalf("certificate changed on restart: %v", err)
	}
	retained, err := ReadToken(filepath.Join(dir, TokenName))
	if err != nil || retained != upgraded {
		t.Fatalf("token changed on restart: %v", err)
	}
}
