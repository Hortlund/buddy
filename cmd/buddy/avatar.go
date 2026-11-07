package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"image"
	_ "image/jpeg"
	"image/png"
	"io"
	"math"
	"os"
	"time"

	"buddy/internal/tasks"

	"github.com/disintegration/imaging"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type blobPalette struct {
	name                    string
	light, base, shade, eye ui.Color
}

var blobPalettes = []blobPalette{
	{"ink", ui.Hex("#414147"), ui.Hex("#151518"), ui.Hex("#070709"), ui.Hex("#F5F5F6")},
	{"pearl", ui.Hex("#FFFFFF"), ui.Hex("#E2E2E5"), ui.Hex("#A0A0A8"), ui.Hex("#171719")},
	{"lilac", ui.Hex("#ECE2FF"), ui.Hex("#B6A1E2"), ui.Hex("#75629E"), ui.Hex("#252037")},
	{"peach", ui.Hex("#FFF0DB"), ui.Hex("#EEBD9D"), ui.Hex("#B67C61"), ui.Hex("#33221C")},
	{"mint", ui.Hex("#E5F7E9"), ui.Hex("#A6D1BB"), ui.Hex("#6B9E87"), ui.Hex("#1D3028")},
	{"sky", ui.Hex("#E5F1FF"), ui.Hex("#ADC8E8"), ui.Hex("#6D8BB5"), ui.Hex("#202B3B")},
	{"rose", ui.Hex("#FFE0F0"), ui.Hex("#DE8EB7"), ui.Hex("#A94A7B"), ui.Hex("#3F1B30")},
	{"coral", ui.Hex("#FFD3C5"), ui.Hex("#E47D69"), ui.Hex("#A6443A"), ui.Hex("#431E1A")},
	{"honey", ui.Hex("#FFF2B7"), ui.Hex("#ECC460"), ui.Hex("#AE772C"), ui.Hex("#3D2915")},
	{"moss", ui.Hex("#E2E9BC"), ui.Hex("#A4B56F"), ui.Hex("#607344"), ui.Hex("#25321A")},
	{"teal", ui.Hex("#A6E5D8"), ui.Hex("#398E85"), ui.Hex("#14565D"), ui.Hex("#F3FFF8")},
	{"cobalt", ui.Hex("#93AEFF"), ui.Hex("#4164CB"), ui.Hex("#24346E"), ui.Hex("#F5F7FF")},
	{"plum", ui.Hex("#BE98D2"), ui.Hex("#774F96"), ui.Hex("#452759"), ui.Hex("#FFF2FF")},
	{"slate", ui.Hex("#B4C3CF"), ui.Hex("#606F82"), ui.Hex("#323D50"), ui.Hex("#F3F7FF")},
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
	orbIdle     orbMood = ""
	orbHappy    orbMood = "happy"
	orbProud    orbMood = "proud"
	orbUnsure   orbMood = "unsure"
	orbSpeaking orbMood = "speaking"
)

func avatar(c *ui.Context, size float32, look tasks.Avatar, bitmap *ui.Bitmap, live bool, mood orbMood) {
	box := ui.Box(c).Size(size, size).Shrink(0)
	var seconds float64
	moving := live && look.Animate && !c.Preferences().ReduceMotion
	if moving {
		type motionClock struct {
			mood  orbMood
			start time.Time
		}
		now := c.Now()
		clock := ui.Local(box, "motion", func() motionClock { return motionClock{mood, now} })
		if clock.mood != mood {
			*clock = motionClock{mood, now}
		}
		seconds = now.Sub(clock.start).Seconds()
		c.After(33 * time.Millisecond)
	}
	box.Draw(func(p *ui.Painter, r ui.Rect) {
		if bitmap != nil {
			lift := float32(math.Sin(seconds*math.Pi)) * r.H * .027
			inset := r.W * .075
			picture := ui.Rect{X: r.X + inset, Y: r.Y + inset - lift, W: r.W - 2*inset, H: r.H - 2*inset}
			p.Clip(picture, r.W*.28, func() { p.Image(bitmap, picture, ui.Cover) })
			return
		}
		drawBlob(p, r, look, seconds, mood)
	})
}

func smoothstep(t float64) float32 {
	t = max(0, min(1, t))
	return float32(t * t * (3 - 2*t))
}

func drawBlob(p *ui.Painter, r ui.Rect, look tasks.Avatar, seconds float64, mood orbMood) {
	palette := blobColors(look.Color)
	wave := func(period float64) float32 { return float32(math.Sin(2 * math.Pi * seconds / period)) }
	lookX, lookY := wave(6)*2.5, wave(4)*1.1
	lift, squash, roll := wave(3)*1.3, wave(3)*.65, wave(6)*2.5
	eyes := [2]blobEye{{x: 53, y: 42, length: 9, width: 8}, {x: 68, y: 38, length: 8, width: 6.8}}
	switch mood {
	case orbMood(tasks.Listening), orbMood(tasks.Reasoning):
		lookX, lookY, roll = wave(3)*3, -1+wave(3)*1.3, wave(3)*6
		eyes[1].length += 2
	case orbMood(tasks.Reading):
		lookX, lookY = wave(2.7)*8, 3+wave(3.8)*1.5
		eyes[0].squint, eyes[1].squint = .26, .26
	case orbMood(tasks.Searching):
		lookX, lookY, roll = wave(2.4)*7, -2+wave(3.2)*2.5, wave(2.4)*9
		lift, squash = max(0, wave(2.4))*3, wave(2.4)*1.6
		eyes[0].length += 2
		eyes[1].length += 4
	case orbMood(tasks.Editing):
		lookX, lookY, roll = wave(1.9)*4, 4+wave(2.6)*1.5, wave(2.6)*4
		eyes[0].squint, eyes[1].squint = .58, .38
	case orbMood(tasks.RunningTool):
		cycle := math.Mod(seconds, 8)
		roll = wave(4)*5 + 360*smoothstep((cycle-.9)/1.35)
		lift, squash = max(0, wave(2))*3.5, wave(2)*1.5
		lookX, lookY = wave(4)*4, wave(3)*2
		eyes[0].squint, eyes[1].squint = .3, .2
	case orbSpeaking:
		lift = max(0, wave(.72))*3.7 + wave(2.1)*1.5
		squash, roll = wave(.72)*2.1, wave(1.9)*7
		eyes[0].length += wave(.78)*2 + 2
		eyes[1].length += wave(.94)*2 + 1
	case orbMood(tasks.Replying), orbHappy:
		lift, squash = max(0, wave(1.7))*4, wave(1.7)*1.8
		for i := range eyes {
			eyes[i].squint, eyes[i].curve = .9, -6.5
		}
	case orbProud:
		lift, squash, roll = max(0, wave(1.55))*5, wave(1.55)*1.8, wave(2.6)*8
		eyes[1].squint, eyes[1].curve = .95, -5
	case orbMood(tasks.Stopping), orbUnsure:
		lookX, lookY, lift = 0, 5, -1.5
		for i := range eyes {
			eyes[i].squint, eyes[i].curve = .8, 4.5
		}
	}
	x := func(x float32) float32 { return r.X + x*r.W/100 }
	y := func(y float32) float32 { return r.Y + (y-lift)*r.H/100 }
	body := blobOutline(look.Shape, squash, x, y)
	blinkPhase := math.Mod(seconds, 4.6)
	blink := smoothstep((blinkPhase-3.4)/.085) * (1 - smoothstep((blinkPhase-3.51)/.13))
	p.Transform(ui.Rotation(roll), x(49), y(50), func() {
		p.FillPathGradient(body, ui.LinearGradient{Angle: 145, Oklab: true, Stops: []ui.GradientStop{
			{Color: palette.light, At: 0}, {Color: palette.base, At: .47}, {Color: palette.shade, At: 1},
		}})
		for _, eye := range eyes {
			midX, midY := eye.x+lookX, eye.y+lookY
			closed := max(blink, eye.squint)
			angle := (77 - 82*closed) * math.Pi / 180
			half := (eye.length + (10-eye.length)*closed) / 2
			dx, dy := half*float32(math.Cos(float64(angle))), half*float32(math.Sin(float64(angle)))
			curve := eye.curve*closed + blink*(1-eye.squint)*3
			lid := new(ui.Path).MoveTo(x(midX-dx), y(midY-dy)).QuadTo(x(midX), y(midY+curve), x(midX+dx), y(midY+dy))
			width := eye.width + (3.2-eye.width)*closed
			p.StrokePath(lid, r.W*width/100, palette.eye)
		}
	})
}

type blobEye struct {
	x, y, length, width, squint, curve float32
}

func blobOutline(shape string, squash float32, x, y func(float32) float32) *ui.Path {
	switch shape {
	case "bean":
		return new(ui.Path).MoveTo(x(56), y(13+squash)).
			CubeTo(x(78), y(8+squash), x(89+squash), y(28), x(85+squash), y(50)).
			CubeTo(x(80+squash), y(73), x(56), y(90-squash), x(34), y(86-squash)).
			CubeTo(x(13-squash), y(82-squash), x(6-squash), y(64), x(15-squash), y(48)).
			CubeTo(x(23-squash), y(34), x(31), y(20+squash), x(56), y(13+squash)).Close()
	case "drop":
		return new(ui.Path).MoveTo(x(49), y(11+squash)).
			CubeTo(x(60), y(11+squash), x(89+squash), y(36), x(89+squash), y(60)).
			CubeTo(x(89+squash), y(80-squash), x(72), y(89-squash), x(49), y(89-squash)).
			CubeTo(x(26), y(89-squash), x(9-squash), y(80-squash), x(9-squash), y(60)).
			CubeTo(x(9-squash), y(36), x(38), y(11+squash), x(49), y(11+squash)).Close()
	case "cloud":
		return new(ui.Path).MoveTo(x(30), y(30+squash)).
			CubeTo(x(30), y(8+squash), x(62), y(5+squash), x(69), y(25+squash)).
			CubeTo(x(86+squash), y(19), x(95+squash), y(34), x(89+squash), y(51)).
			CubeTo(x(101+squash), y(71), x(83), y(88-squash), x(66), y(81-squash)).
			CubeTo(x(57), y(94-squash), x(34), y(94-squash), x(25), y(81-squash)).
			CubeTo(x(3-squash), y(84-squash), x(3-squash), y(62), x(12-squash), y(53)).
			CubeTo(x(7-squash), y(39), x(17), y(26+squash), x(30), y(30+squash)).Close()
	case "capsule":
		left, right, radius := 9-squash, 89+squash, 28-squash
		top, bottom := 50-radius, 50+radius
		return new(ui.Path).MoveTo(x(left+radius), y(top)).LineTo(x(right-radius), y(top)).
			CubeTo(x(right+radius*.333), y(top), x(right+radius*.333), y(bottom), x(right-radius), y(bottom)).
			LineTo(x(left+radius), y(bottom)).
			CubeTo(x(left-radius*.333), y(bottom), x(left-radius*.333), y(top), x(left+radius), y(top)).Close()
	case "pillow":
		left, right, top, bottom := 12-squash, 86+squash, 13+squash, 87-squash
		return new(ui.Path).MoveTo(x(49), y(top+4)).
			CubeTo(x(72), y(top+4), x(right+6), y(top-11), x(right-3), y(50)).
			CubeTo(x(right+6), y(bottom+11), x(72), y(bottom-4), x(49), y(bottom-4)).
			CubeTo(x(26), y(bottom-4), x(left-6), y(bottom+11), x(left+3), y(50)).
			CubeTo(x(left-6), y(top-11), x(26), y(top+4), x(49), y(top+4)).Close()
	}
	cx, cy := float32(49), float32(50)
	rx, ry, round := 40+squash, 40-squash, float32(.552285)
	if shape == "pebble" {
		rx, ry, round = 39+squash, 34-squash, .87
	}
	return new(ui.Path).MoveTo(x(cx), y(cy-ry)).
		CubeTo(x(cx+rx*round), y(cy-ry), x(cx+rx), y(cy-ry*round), x(cx+rx), y(cy)).
		CubeTo(x(cx+rx), y(cy+ry*round), x(cx+rx*round), y(cy+ry), x(cx), y(cy+ry)).
		CubeTo(x(cx-rx*round), y(cy+ry), x(cx-rx), y(cy+ry*round), x(cx-rx), y(cy)).
		CubeTo(x(cx-rx), y(cy-ry*round), x(cx-rx*round), y(cy-ry), x(cx), y(cy-ry)).Close()
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
				ui.Text(c, "Give your companion its own look.").TextColor(c.Theme().TextMuted).FontSize(12)
				ui.Row(c).Gap(8).Wrap().Children(func() {
					ui.Button(c.Key("choose-picture"), "Choose picture…").Disabled(a.picturePicking || a.saving).OnClick(a.choosePicture)
					if len(look.Image) != 0 {
						ui.Button(c.Key("use-blob"), "Use blob").OnClick(func() { look.Image = nil })
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
						button := ui.ButtonBase(c.Key("avatar-shape-"+choice.id)).Label(choice.label).Selected(selected).
							Height(32).Padding(0, 7).Gap(4).Radius(7).Border(1, c.Theme().Border).
							OnClick(func() { look.Shape = choice.id })
						if selected {
							button.Border(1, c.Theme().Text).Background(c.Theme().SurfacePressed)
						} else if button.Hovered() {
							button.Background(c.Theme().SurfaceHover)
						}
						button.Children(func() {
							avatar(c, 23, tasks.Avatar{Color: look.Color, Shape: choice.id}, nil, false, orbIdle)
							ui.Text(c, choice.label).FontSize(11).FontWeight(600).TextColor(c.Theme().Text)
						})
					}
				})
			})
		}
		ui.Checkbox(c.Key("avatar-motion"), &look.Animate, "Animate profile picture")
	})
}
