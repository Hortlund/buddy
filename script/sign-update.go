// Sign the custom Buddy bundle in the manifest format of the pinned MyGo
// updater. Signing stays in CI; the app uses MyGo's own verifier and installer.
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type updateManifest struct {
	Version   string `json:"version"`
	Notes     string `json:"notes"`
	Date      string `json:"date"`
	URL       string `json:"url"`
	Size      int64  `json:"size"`
	Signature string `json:"signature"`
}

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "Usage: sign-update ARCHIVE MANIFEST VERSION")
		os.Exit(1)
	}
	if err := signUpdate(os.Args[1], os.Args[2], os.Args[3], os.Getenv("MYGO_UPDATER_PRIVATE_KEY"), os.Getenv("BUDDY_UPDATE_PUBLIC_KEY")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func signUpdate(archivePath, manifestPath, version, privateText, publicText string) error {
	private, err := base64.StdEncoding.DecodeString(strings.TrimSpace(privateText))
	if err != nil || len(private) != ed25519.PrivateKeySize {
		return fmt.Errorf("invalid MyGo update private key")
	}
	public, err := base64.StdEncoding.DecodeString(strings.TrimSpace(publicText))
	if err != nil || len(public) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid MyGo update public key")
	}
	key := ed25519.PrivateKey(private)
	if !bytes.Equal(ed25519.NewKeyFromSeed(key.Seed()), key) || !bytes.Equal(key.Public().(ed25519.PublicKey), public) {
		return fmt.Errorf("MyGo update signing keys do not match; refusing to publish")
	}
	archive, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer archive.Close()
	digest := sha256.New()
	size, err := io.Copy(digest, archive)
	if err != nil {
		return err
	}
	signature := ed25519.Sign(key, digest.Sum(nil))
	if !ed25519.Verify(public, digest.Sum(nil), signature) {
		return fmt.Errorf("MyGo signature verification failed")
	}
	manifest := updateManifest{
		Version:   version,
		Notes:     "See the [release notes](https://github.com/Hortlund/buddy/releases/tag/v" + version + ").",
		Date:      time.Now().UTC().Format(time.RFC3339),
		URL:       "https://github.com/Hortlund/buddy/releases/download/v" + version + "/" + filepath.Base(archivePath),
		Size:      size,
		Signature: base64.StdEncoding.EncodeToString(signature),
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(manifestPath, append(data, '\n'), 0644)
}
