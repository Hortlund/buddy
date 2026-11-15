package main

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"buddy/internal/tasks"

	"github.com/egoist/mygo/ui"
)

func TestReadProfilePictureCropsAndResizes(t *testing.T) {
	photo := image.NewRGBA(image.Rect(0, 0, 480, 240))
	draw.Draw(photo, photo.Bounds(), image.NewUniform(color.RGBA{R: 255, A: 255}), image.Point{}, draw.Src)
	draw.Draw(photo, image.Rect(120, 0, 360, 240), image.NewUniform(color.RGBA{B: 255, A: 255}), image.Point{}, draw.Src)
	var jpegData bytes.Buffer
	if err := jpeg.Encode(&jpegData, photo, nil); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "profile.jpg")
	if err := os.WriteFile(path, jpegData.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := readProfilePicture(path)
	if err != nil {
		t.Fatal(err)
	}
	avatar, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if got := avatar.Bounds(); got != image.Rect(0, 0, 160, 160) {
		t.Fatalf("avatar bounds = %v", got)
	}
	for _, x := range []int{10, 150} {
		r, _, b, _ := avatar.At(x, 80).RGBA()
		if r >= b {
			t.Fatalf("avatar retained an outer edge at x=%d", x)
		}
	}
}

func TestReadProfilePictureAppliesJPEGOrientation(t *testing.T) {
	photo := image.NewRGBA(image.Rect(0, 0, 240, 160))
	draw.Draw(photo, photo.Bounds(), image.NewUniform(color.RGBA{B: 255, A: 255}), image.Point{}, draw.Src)
	draw.Draw(photo, image.Rect(0, 0, 120, 160), image.NewUniform(color.RGBA{R: 255, A: 255}), image.Point{}, draw.Src)
	var jpegData bytes.Buffer
	if err := jpeg.Encode(&jpegData, photo, nil); err != nil {
		t.Fatal(err)
	}
	// APP1 with a single little-endian EXIF tag for a 90° clockwise rotation.
	exif := []byte("\xff\xe1\x00\x22Exif\x00\x00II\x2a\x00\x08\x00\x00\x00\x01\x00" +
		"\x12\x01\x03\x00\x01\x00\x00\x00\x06\x00\x00\x00\x00\x00\x00\x00")
	data := append(append(append([]byte{}, jpegData.Bytes()[:2]...), exif...), jpegData.Bytes()[2:]...)
	path := filepath.Join(t.TempDir(), "oriented.jpg")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	picture, err := readProfilePicture(path)
	if err != nil {
		t.Fatal(err)
	}
	avatar, err := png.Decode(bytes.NewReader(picture))
	if err != nil {
		t.Fatal(err)
	}
	topRed, _, topBlue, _ := avatar.At(80, 20).RGBA()
	bottomRed, _, bottomBlue, _ := avatar.At(80, 140).RGBA()
	if topRed <= topBlue || bottomBlue <= bottomRed {
		t.Fatal("avatar did not rotate the left and right halves to the top and bottom")
	}
}

func TestPictureImportStaysInItsSettingsSession(t *testing.T) {
	a := &desktop{}
	a.showSettingsFor(a.focusedAgent())
	cancelled := a.edit.Avatar
	a.section = ""
	a.showSettingsFor(a.focusedAgent())
	a.picturePicking = true
	a.finishPicture(cancelled, []byte("old picture"), nil)
	if len(a.edit.Avatar.Image) != 0 || !a.picturePicking {
		t.Fatal("a cancelled import changed the current settings session")
	}
	a.finishPicture(a.edit.Avatar, []byte("new picture"), nil)
	if string(a.edit.Avatar.Image) != "new picture" || a.picturePicking {
		t.Fatal("the current import did not complete")
	}
}

func TestAvatarFollowsNearbyPointerAndRespectsReduceMotion(t *testing.T) {
	view := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(darkBuddyTheme)
		ui.Row(c).Fill().Children(func() {
			ui.Box(c).Width(130)
			avatar(c, 96, tasks.DefaultAvatar(), nil, true, orbIdle)
		})
	}, 470, 96)
	center := func() image.Point {
		t.Helper()
		frame := view.Image()
		var xSum, ySum, count int
		for y := 18; y < 78; y++ {
			for x := 12; x < 86; x++ {
				if frame.RGBAAt(x+130, y).R > 225 {
					xSum, ySum, count = xSum+x, ySum+y, count+1
				}
			}
		}
		if count == 0 {
			t.Fatal("could not find the eyes")
		}
		return image.Pt(xSum/count, ySum/count)
	}
	neutral := center()
	// Stay ahead of the first idle blink so the eye centroid measures gaze.
	const settle = 400 * time.Millisecond
	view.Move(103, 48)
	view.Advance(settle)
	left := center()
	view.Move(253, 48)
	view.Advance(settle)
	right := center()
	if right.X < left.X+10 {
		t.Fatalf("eyes did not follow a nearby pointer outside the avatar: %v to %v", left, right)
	}
	view.Move(415, 48)
	view.Advance(settle)
	farRight := center()
	view.Move(12, 48)
	view.Advance(settle)
	farLeft := center()
	if farRight.X < neutral.X-3 || farRight.X > neutral.X+3 || farRight.X-farLeft.X > 2 || farLeft.X-farRight.X > 2 {
		t.Fatalf("eyes tracked the distant pointer: left %v, right %v, neutral %v", farLeft, farRight, neutral)
	}
	view.SetPreferences(ui.Preferences{ReduceMotion: true})
	still := center()
	view.Move(103, 48)
	view.Advance(1575 * time.Millisecond)
	if got := center(); got != still {
		t.Fatalf("eyes moved with Reduce Motion enabled: %v to %v", still, got)
	}
}
