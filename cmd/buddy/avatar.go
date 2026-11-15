package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"image"
	"image/color"
	_ "image/jpeg"
	"image/png"
	"io"
	"math"
	"os"
	"time"

	"buddy/internal/budmodel"
	"buddy/internal/tasks"

	"github.com/disintegration/imaging"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type blobPalette struct {
	name      string
	base, eye ui.Color
}

// BudFace on iPhone uses the same materials.
var blobPalettes = []blobPalette{
	{"ink", ui.Hex("#4C494F"), ui.Hex("#F6F1F0")},
	{"pearl", ui.Hex("#EBE2D5"), ui.Hex("#2B2424")},
	{"lilac", ui.Hex("#C5AAFA"), ui.Hex("#30213F")},
	{"peach", ui.Hex("#FFBC91"), ui.Hex("#351F1B")},
	{"mint", ui.Hex("#98DDBD"), ui.Hex("#15332C")},
	{"sky", ui.Hex("#98D0F7"), ui.Hex("#1D2C47")},
	{"rose", ui.Hex("#F49DC5"), ui.Hex("#3A1E32")},
	{"coral", ui.Hex("#FF8856"), ui.Hex("#2D191A")},
	{"honey", ui.Hex("#F6C858"), ui.Hex("#362819")},
	{"moss", ui.Hex("#BDD07F"), ui.Hex("#29331F")},
	{"teal", ui.Hex("#57BDAC"), ui.Hex("#123331")},
	{"cobalt", ui.Hex("#779BFF"), ui.Hex("#1B2449")},
	{"plum", ui.Hex("#AE83DC"), ui.Hex("#29213D")},
	{"slate", ui.Hex("#768494"), ui.Hex("#18212D")},
}

var blobShapes = []struct{ id, label string }{
	{"orb", "Orb"}, {"bean", "Bean"}, {"pebble", "Pebble"}, {"drop", "Drop"},
	{"cloud", "Cloud"}, {"capsule", "Capsule"}, {"pillow", "Pillow"},
}

func blobColors(name string) blobPalette {
	for _, colors := range blobPalettes {
		if colors.name == name {
			return colors
		}
	}
	return blobPalettes[0]
}

type avatarCache struct {
	key    [32]byte
	bitmap *ui.Bitmap
}

func (a *avatarCache) image(data []byte) *ui.Bitmap {
	if len(data) == 0 {
		return nil
	}
	if key := sha256.Sum256(data); a.key != key {
		a.key = key
		a.bitmap, _ = ui.DecodeBitmap(data)
	}
	return a.bitmap
}

func avatarAppearance(agent tasks.Agent) tasks.Avatar {
	if agent.Avatar == nil {
		return tasks.DefaultAvatar()
	}
	return *agent.Avatar
}

type orbMood string

const (
	orbIdle          orbMood = ""
	orbSpeaking      orbMood = "speaking"
	orbSidebarActive orbMood = "sidebar-active"
)

type characterFrame struct {
	director     budmodel.Director
	renderer     budmodel.Renderer
	bitmap       *ui.Bitmap
	at           time.Time
	pose         budmodel.Pose
	shape, color string
	resolution   int
}

func avatar(c *ui.Context, size float32, look tasks.Avatar, bitmap *ui.Bitmap, live bool, mood orbMood) {
	box := ui.Box(c).Size(size, size).Shrink(0).PassThrough()
	if bitmap != nil {
		box.Draw(func(p *ui.Painter, r ui.Rect) {
			inset := r.W * .075
			picture := ui.Rect{X: r.X + inset, Y: r.Y + inset, W: r.W - 2*inset, H: r.H - 2*inset}
			p.Clip(picture, r.W*.28, func() { p.Image(bitmap, picture, ui.Cover) })
		})
		return
	}

	now := c.Now()
	frame := ui.Local(box, "character", func() characterFrame { return characterFrame{} })
	var pose budmodel.Pose
	portrait := size < 32 && !live
	if live && look.Animate && !c.Preferences().ReduceMotion {
		var aimX, aimY float32
		if size >= 75 && c.Preferences().Hover() {
			_, _, inside := c.Root().PointerPosition()
			x, y, _ := box.PointerPosition()
			if inside {
				aimX, aimY = pointerAim(x-size/2, y-size/2, size)
			}
		}
		pose = frame.director.Sample(now, characterAction(mood), aimX, aimY, size < 45)
		rate := 30
		if size < 32 {
			rate = 20
		}
		c.After(time.Second / time.Duration(rate))
	} else {
		frame.director = budmodel.Director{}
	}
	resolution := min(256, int(size*2))
	changed := frame.bitmap == nil || frame.shape != look.Shape || frame.color != look.Color || frame.resolution != resolution
	if changed || (frame.pose != pose && (pose == (budmodel.Pose{}) || now.Sub(frame.at) >= time.Second/34)) {
		palette := blobColors(look.Color)
		rgba := func(c ui.Color) color.RGBA { return color.RGBA{R: c.R, G: c.G, B: c.B, A: 255} }
		pixels := frame.renderer.Draw(resolution, look.Shape, rgba(palette.base), rgba(palette.eye), pose, portrait)
		if frame.bitmap == nil {
			frame.bitmap = ui.NewBitmap(pixels)
		} else {
			frame.bitmap.Update(pixels)
		}
		frame.at, frame.pose = now, pose
		frame.shape, frame.color, frame.resolution = look.Shape, look.Color, resolution
	}
	box.Draw(func(p *ui.Painter, r ui.Rect) { p.Image(frame.bitmap, r, ui.Contain) })
}

func pointerAim(dx, dy, size float32) (float32, float32) {
	distance := math.Hypot(float64(dx), float64(dy))
	inner, outer := float64(size)*.9, float64(size)*1.65
	falloff := max(0, min(1, (distance-inner)/(outer-inner)))
	strength := 1 - falloff*falloff*(3-2*falloff)
	return float32(float64(dx) / (distance + 30) * strength), float32(float64(dy) / (distance + 30) * strength)
}

func characterAction(mood orbMood) budmodel.Action {
	switch mood {
	case orbMood(tasks.Reasoning), orbMood(tasks.Starting):
		return budmodel.Think
	case orbMood(tasks.Reading), orbMood(tasks.Editing):
		return budmodel.Notebook
	case orbMood(tasks.Searching), orbMood(tasks.RunningTool):
		return budmodel.Investigate
	case orbMood(tasks.Replying):
		return budmodel.Idea
	case orbMood(tasks.Listening):
		return budmodel.Nod
	case orbSpeaking:
		return budmodel.Talk
	case orbSidebarActive:
		return budmodel.DoubleHop
	default:
		return budmodel.Rest
	}
}

func (a *desktop) choosePicture() {
	if a.picturePicking || a.window == nil {
		return
	}
	a.picturePicking = true
	win, draft := a.window, a.edit.Avatar
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent: win, Title: "Choose a profile picture", ButtonLabel: "Choose Picture",
			Filters: []mygo.FileFilter{{Name: "Profile pictures", Extensions: []string{"png", "jpg", "jpeg"}}},
		})
		var picture []byte
		if err == nil && len(paths) == 1 {
			picture, err = readProfilePicture(paths[0])
		}
		win.Update(func() { a.finishPicture(draft, picture, err) })
	}()
}

func (a *desktop) finishPicture(draft *tasks.Avatar, picture []byte, err error) {
	if a.section != "agent" || a.edit.Avatar != draft {
		return
	}
	a.picturePicking = false
	if err != nil {
		a.editErr = err.Error()
	} else if picture != nil {
		draft.Image = picture
		a.editErr = ""
	}
}

func readProfilePicture(path string) ([]byte, error) {
	const maxFile = 10 << 20
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("Couldn't open that picture.")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxFile+1))
	if err != nil || len(data) > maxFile {
		return nil, errors.New("Choose a PNG or JPEG under 10 MB.")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg") ||
		cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 5000 || cfg.Height > 5000 ||
		cfg.Width*cfg.Height > 16_000_000 {
		return nil, errors.New("Choose a PNG or JPEG up to 16 megapixels.")
	}
	src, err := imaging.Decode(bytes.NewReader(data), imaging.AutoOrientation(true))
	if err != nil {
		return nil, errors.New("Couldn't read that picture.")
	}
	dst := imaging.Fill(src, 160, 160, imaging.Center, imaging.CatmullRom)
	var output bytes.Buffer
	if err := png.Encode(&output, dst); err != nil || output.Len() > tasks.MaxAvatarImage {
		return nil, errors.New("Couldn't prepare that picture.")
	}
	return output.Bytes(), nil
}

func (a *desktop) appearanceSettings(c *ui.Context) {
	look := a.edit.Avatar
	ui.Column(c).Padding(18).Gap(12).Background(c.Theme().Surface).Border(1, c.Theme().Border).Radius(13).Children(func() {
		ui.Row(c).Gap(14).Children(func() {
			avatar(c, 78, *look, a.editAvatar.image(look.Image), true, orbIdle)
			ui.Column(c).Gap(5).Grow(1).Children(func() {
				ui.Text(c, "Profile picture").FontSize(16).Bold()
				ui.Row(c).Gap(8).Wrap().Children(func() {
					ui.Button(c.Key("choose-picture"), "Choose picture…").Disabled(a.picturePicking || a.saving).OnClick(a.choosePicture)
					if len(look.Image) != 0 {
						ui.Button(c.Key("use-blob"), "Use character").OnClick(func() { look.Image = nil })
					}
				})
			})
		})
		if len(look.Image) == 0 {
			ui.Column(c).Gap(6).Children(func() {
				ui.Text(c, "Color").FontWeight(650)
				ui.Row(c).Gap(5).Wrap().Children(func() {
					for _, palette := range blobPalettes {
						swatch := ui.ButtonBase(c.Key("avatar-color-"+palette.name)).Size(28, 28).Radius(14).Center().
							Label(palette.name).Tooltip(palette.name).Selected(look.Color == palette.name).
							OnClick(func() { look.Color = palette.name })
						if look.Color == palette.name {
							swatch.Border(1, c.Theme().Text)
						} else if swatch.Hovered() {
							swatch.Background(c.Theme().SurfacePressed)
						}
						swatch.Children(func() {
							ui.Box(c).Size(22, 22).Radius(11).Background(palette.base).Border(1, c.Theme().Text.Alpha(.12))
						})
					}
				})
			})
			ui.Column(c).Gap(6).Children(func() {
				ui.Text(c, "Shape").FontWeight(650)
				ui.Row(c).Gap(5).Wrap().Children(func() {
					for _, choice := range blobShapes {
						selected := look.Shape == choice.id || look.Shape == "" && choice.id == "orb"
						button := ui.ButtonBase(c.Key("avatar-shape-"+choice.id)).Label(choice.label).Tooltip(choice.label).Selected(selected).
							Size(48, 46).Center().Radius(11).
							OnClick(func() { look.Shape = choice.id })
						if selected {
							button.Border(1, c.Theme().Text).Background(c.Theme().SurfacePressed)
						} else if button.Hovered() {
							button.Background(c.Theme().SurfaceHover)
						}
						button.Children(func() {
							avatar(c, 38, tasks.Avatar{Color: look.Color, Shape: choice.id}, nil, false, orbIdle)
						})
					}
				})
			})
		}
		ui.Checkbox(c.Key("avatar-motion"), &look.Animate, "Animate profile picture")
	})
}
