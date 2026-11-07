package main

import (
	"bytes"
	"image"
	"image/jpeg"
	"testing"
)

func TestWorkspaceImageHonorsJPEGOrientation(t *testing.T) {
	var original bytes.Buffer
	if err := jpeg.Encode(&original, image.NewGray(image.Rect(0, 0, 2, 3)), nil); err != nil {
		t.Fatal(err)
	}
	// APP1 with a little-endian EXIF Orientation tag of 6 (90° clockwise).
	exif := "\xff\xe1\x00\x22Exif\x00\x00II\x2a\x00\x08\x00\x00\x00" +
		"\x01\x00\x12\x01\x03\x00\x01\x00\x00\x00\x06\x00\x00\x00\x00\x00\x00\x00"
	data := original.String()
	picture := decodeWorkspaceImage([]byte(data[:2] + exif + data[2:]))
	if picture == nil {
		t.Fatal("could not decode the JPEG")
	}
	if w, h := picture.Size(); w != 3 || h != 2 {
		t.Fatalf("got %dx%d, want 3x2 after rotation", w, h)
	}
}
