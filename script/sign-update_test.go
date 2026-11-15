package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMyGoManifestUsesSignedDigestAndMatchingKeys(t *testing.T) {
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	archive, manifest := filepath.Join(dir, "buddy-0.2.0-darwin-universal.tar.gz"), filepath.Join(dir, "update-darwin-universal.json")
	contents := []byte("the signed archive")
	if err := os.WriteFile(archive, contents, 0600); err != nil {
		t.Fatal(err)
	}
	pub, priv := base64.StdEncoding.EncodeToString(public), base64.StdEncoding.EncodeToString(private)
	if err := signUpdate(archive, manifest, "0.2.0", priv, pub); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var result updateManifest
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(contents)
	signature, err := base64.StdEncoding.DecodeString(result.Signature)
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(public, digest[:], signature) || result.Size != int64(len(contents)) || result.Version != "0.2.0" {
		t.Fatalf("invalid MyGo update manifest: %+v", result)
	}
	other, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := signUpdate(archive, manifest, "0.2.0", priv, base64.StdEncoding.EncodeToString(other)); err == nil {
		t.Fatal("signed a release with mismatched keys")
	}
	changed := sha256.Sum256([]byte("changed archive"))
	if ed25519.Verify(public, changed[:], signature) {
		t.Fatal("accepted changed archive")
	}
}
