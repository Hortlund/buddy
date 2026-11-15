package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/png"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/egoist/mygo/plugins/render"
	"github.com/egoist/mygo/ui"
)

// Optional visual artifacts use only the fictional data in these tests.
func saveUIProof(t *testing.T, name string, view *ui.Tester) {
	t.Helper()
	if os.Getenv("BUDDY_UI_PROOF_DIR") == "" {
		return
	}
	var data bytes.Buffer
	if err := png.Encode(&data, view.Image()); err != nil {
		t.Fatal(err)
	}
	saveExportProof(t, name, data.Bytes())
}

func saveExportProof(t *testing.T, name string, data []byte) {
	t.Helper()
	dir := os.Getenv("BUDDY_UI_PROOF_DIR")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestExportLinesKeepsUnicodeAndEmptyLines(t *testing.T) {
	input := strings.Repeat("界", 74) + "\r\n\r\nend\tfix"
	lines := exportLines(input)
	if strings.Join(lines, "") != strings.Repeat("界", 74)+"end fix" || len(lines) != 5 {
		t.Fatalf("export lost content or blank lines: %#v", lines)
	}
	for _, line := range lines {
		if !utf8.ValidString(line) {
			t.Fatal("wrapped inside a UTF-8 rune")
		}
	}
}

func TestMediaExportWritesPNGAndKeepsExistingFileOnCancellation(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "reply.png")
	doc := exportDocument{title: "Buddy · Quick fix", text: "A small change.\n\n" + strings.Repeat("Last line stays in the image.\n", 22)}
	if err := writeDocumentExport(t.Context(), doc, output, false, render.Options{Renderer: render.CPU}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	image, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || image.Width != 1600 || image.Height <= 900 {
		t.Fatalf("export is not the complete tall PNG: %+v, %v", image, err)
	}
	saveExportProof(t, "reply.png", data)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := writeDocumentExport(ctx, doc, output, false, render.Options{Renderer: render.CPU}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled export returned %v", err)
	}
	after, err := os.ReadFile(output)
	if err != nil || !bytes.Equal(data, after) {
		t.Fatal("cancellation overwrote the existing export")
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, ".buddy-export-*"))
	if len(leftovers) != 0 {
		t.Fatalf("canceled export left temporary files: %v", leftovers)
	}
}

func TestMediaExportRejectsClippedImagesAndLimitsVideo(t *testing.T) {
	if _, err := documentComposition(exportDocument{text: strings.Repeat("line\n", 500)}, false); err == nil {
		t.Fatal("large image would silently clip its text")
	}
	if _, err := documentComposition(exportDocument{text: strings.Repeat("line\n", 1300)}, true); err == nil {
		t.Fatal("unbounded video accepted")
	}
	comp, err := documentComposition(exportDocument{text: strings.Repeat("line\n", 20)}, true)
	if err != nil || comp.Frames != 288 {
		t.Fatalf("video omitted the second page: %+v, %v", comp, err)
	}
}

func TestMediaExportWritesNativeMP4(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native video encoding is checked on macOS without optional Linux encoders")
	}
	output := filepath.Join(t.TempDir(), "reply.mp4")
	if err := writeDocumentExport(t.Context(), exportDocument{title: "Buddy", text: "A quick manual fix."}, output, true,
		render.Options{Renderer: render.CPU}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil || len(data) < 1000 || !bytes.Contains(data, []byte("ftyp")) ||
		!bytes.Contains(data, []byte("moov")) || !bytes.Contains(data, []byte("avc1")) {
		t.Fatalf("export is not a complete H.264 MP4: %d bytes, %v", len(data), err)
	}
	saveExportProof(t, "reply.mp4", data)
}

// A shared-readable destination must never contain a readable temporary file.
// Encoder files may use their normal mode only inside an inaccessible directory.
func checkExportStaging(t *testing.T, directory, output string) bool {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Error(err)
		return false
	}
	foundEncoder := false
	for _, entry := range entries {
		if entry.Name() == filepath.Base(output) {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			t.Error(err)
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
			t.Errorf("temporary export is accessible to other users: %s (%04o)", path, info.Mode().Perm())
		}
		if err := filepath.WalkDir(path, func(name string, item fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !item.IsDir() && strings.Contains(item.Name(), ".render-") {
				foundEncoder = true
			}
			return nil
		}); err != nil {
			t.Error(err)
		}
	}
	return foundEncoder
}

func TestMediaExportVideoKeepsAllStagesPrivate(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native MP4 encoder is tested on macOS")
	}
	for _, interrupt := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel_%t", interrupt), func(t *testing.T) {
			directory := t.TempDir()
			if err := os.Chmod(directory, 0755); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(directory, "reply.MP4")
			previous := []byte("previous export")
			if err := os.WriteFile(output, previous, 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			checked := false
			err := writeDocumentExport(ctx, exportDocument{title: "Fixture", text: "Fictional private text."}, output, true,
				render.Options{Renderer: render.CPU, Progress: func(render.Progress) {
					if checked {
						return
					}
					checked = true
					if !checkExportStaging(t, directory, output) {
						t.Error("did not inspect the encoder's intermediate file")
					}
					if interrupt {
						cancel()
					}
				}})
			if !checked {
				t.Fatal("encoder progress did not run")
			}
			if interrupt {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("in-progress cancellation: %v", err)
				}
				content, _ := os.ReadFile(output)
				if !bytes.Equal(content, previous) {
					t.Fatal("cancellation replaced the existing export")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(output)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("final export permissions: %v, %v", info, err)
			}
			entries, _ := os.ReadDir(directory)
			if len(entries) != 1 || entries[0].Name() != filepath.Base(output) {
				t.Fatalf("export left temporary files: %v", entries)
			}
		})
	}
}

func TestMediaExportCleansStagesWhenPublicationFails(t *testing.T) {
	for _, video := range []bool{false, true} {
		extension := ".png"
		if video {
			extension = ".mp4"
		}
		t.Run(extension, func(t *testing.T) {
			if video && runtime.GOOS != "darwin" {
				t.Skip("native MP4 encoder is tested on macOS")
			}
			directory := t.TempDir()
			output := filepath.Join(directory, "existing"+extension)
			if err := os.Mkdir(output, 0700); err != nil {
				t.Fatal(err)
			}
			sentinel := filepath.Join(output, "keep.txt")
			if err := os.WriteFile(sentinel, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := writeDocumentExport(t.Context(), exportDocument{text: "Fixture"}, output, video,
				render.Options{Renderer: render.CPU}); err == nil {
				t.Fatal("publication replaced an existing directory")
			}
			content, err := os.ReadFile(sentinel)
			if err != nil || string(content) != "keep" {
				t.Fatalf("failed publication modified the destination: %q, %v", content, err)
			}
			entries, err := os.ReadDir(directory)
			if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(output) {
				t.Fatalf("failed publication left temporary files: %v, %v", entries, err)
			}
		})
	}
}
