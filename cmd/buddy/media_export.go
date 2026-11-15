package main

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/render"
	"github.com/egoist/mygo/ui"
	"golang.org/x/text/width"
)

type exportDocument struct {
	title, text string
	dark        bool
}

type mediaExport struct {
	busy     bool
	progress int
	cancel   context.CancelFunc
	output   string
	err      string
}

func (m *mediaExport) menu(menu *ui.Menu, parent *mygo.Window, ctx context.Context, doc exportDocument) {
	for _, video := range []bool{false, true} {
		label := "Export text as image…"
		if video {
			label = "Export text as video…"
		}
		if menu.Item(label).Disabled(parent == nil || m.busy || strings.TrimSpace(doc.text) == "").Chosen() {
			m.start(parent, ctx, doc, video)
		}
	}
}

func (m *mediaExport) start(parent *mygo.Window, ctx context.Context, doc exportDocument, video bool) {
	if m.busy || parent == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, m.cancel = context.WithCancel(ctx)
	m.busy, m.progress, m.output, m.err = true, 0, "", ""
	go func() {
		extension := "png"
		if video {
			extension = "mp4"
		}
		output, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: parent, Title: "Export Buddy text",
			DefaultPath: defaultDownload("Buddy export." + extension),
			Message:     "Save this text as an image or a paged video. Nothing is uploaded.",
			Filters:     []mygo.FileFilter{{Name: strings.ToUpper(extension), Extensions: []string{extension}}}})
		if err == nil && output != "" {
			if filepath.Ext(output) == "" {
				output += "." + extension
			}
			var updated time.Time
			err = writeDocumentExport(ctx, doc, output, video, render.Options{Progress: func(p render.Progress) {
				if p.Done != p.Frames && time.Since(updated) < 150*time.Millisecond {
					return
				}
				updated = time.Now()
				parent.Update(func() { m.progress = p.Done * 100 / max(1, p.Frames) })
			}})
		}
		parent.Update(func() {
			m.cancel()
			m.busy, m.cancel = false, nil
			if err != nil && !errors.Is(err, context.Canceled) {
				m.err = errorMessage(err, "Couldn't export this text.")
			} else if err == nil {
				m.output = output
			}
		})
	}()
}

func (m *mediaExport) view(c *ui.Context) {
	if !m.busy && m.err == "" && m.output == "" {
		return
	}
	ui.Row(c).FillWidth().Padding(7, 16).Gap(8).Wrap().AlignItems(ui.Center).
		Background(c.Theme().Surface).Children(func() {
		if m.busy {
			ui.Text(c, fmt.Sprintf("Exporting… %d%%", m.progress)).FontSize(12)
			ui.Button(c, "Cancel export").OnClick(func() {
				if m.cancel != nil {
					m.cancel()
				}
			})
			return
		}
		if m.err != "" {
			ui.Text(c, m.err).FontSize(12).TextColor(c.Theme().Danger).Grow(1)
		} else {
			ui.Text(c, "Export saved").FontSize(12).Tooltip(m.output)
			ui.Button(c, "Show in Finder").OnClick(func() { mygo.Shell.ShowItemInFolder(m.output) })
		}
		ui.Button(c, "Dismiss export").OnClick(func() { m.err, m.output = "", "" })
	})
}

// Wrap before paginating so every exported line fits, including CJK and tabs.
// Keep all text; reject overlarge exports rather than silently clipping them.
func exportLines(text string) []string {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		var current strings.Builder
		columns := 0
		for _, r := range line {
			if r == '\t' {
				n := 4 - columns%4
				if columns+n > 72 {
					lines = append(lines, current.String())
					current.Reset()
					columns, n = 0, 4
				}
				current.WriteString(strings.Repeat(" ", n))
				columns += n
				continue
			}
			n := 1
			if kind := width.LookupRune(r).Kind(); kind == width.EastAsianWide || kind == width.EastAsianFullwidth {
				n = 2
			}
			if unicode.Is(unicode.Mn, r) || r == '\u200d' {
				n = 0
			}
			if columns+n > 72 {
				lines = append(lines, current.String())
				current.Reset()
				columns = 0
			}
			current.WriteRune(r)
			columns += n
		}
		lines = append(lines, current.String())
	}
	return lines
}

func documentComposition(doc exportDocument, video bool) (render.Composition, error) {
	lines := exportLines(doc.text)
	const perPage, framesPerPage = 20, 144 // six seconds per page at 24 fps
	pages := (len(lines) + perPage - 1) / perPage
	comp := render.Composition{Width: 1600, Height: 1440, Scale: 2, FPS: 24, Frames: 1, Dark: doc.dark}
	if video {
		if pages > 60 {
			return comp, errors.New("Choose a shorter passage for a video (up to 60 pages).")
		}
		comp.Frames = pages * framesPerPage
	} else {
		comp.Height = max(900, 2*(160+21*len(lines)))
		if comp.Height > 16384 {
			return comp, errors.New("This text is too long for one image. Export a video or choose a shorter passage.")
		}
	}
	comp.View = func(c *ui.Context, frame render.Frame) {
		if doc.dark {
			c.SetTheme(themeFor("dark"))
		} else {
			c.SetTheme(themeFor("light"))
		}
		page, content := 0, lines
		if video {
			page = min(pages-1, frame.Index/framesPerPage)
			content = lines[page*perPage : min(len(lines), (page+1)*perPage)]
		}
		ui.Column(c).Fill().Padding(28, 32).Gap(14).Background(c.Theme().Background).Children(func() {
			ui.Text(c, "Buddy").FontSize(14).FontWeight(650).TextColor(c.Theme().Accent)
			ui.Text(c, doc.title).FontSize(20).FontWeight(650).SingleLine()
			ui.Column(c).Grow(1).FillWidth().MinHeight(0).Padding(18).Radius(12).
				Background(c.Theme().Surface).Border(1, c.Theme().Border).Children(func() {
				ui.Text(c, strings.Join(content, "\n")).Font("monospace").FontSize(14).LineHeight(1.5).FillWidth()
			})
			if video {
				ui.Text(c, fmt.Sprintf("%d / %d", page+1, pages)).FontSize(11).TextColor(c.Theme().TextMuted)
				ui.Box(c).Height(2).Width(736 * float32(frame.Index+1) / float32(comp.Frames)).Background(c.Theme().Accent)
			}
		})
	}
	return comp, nil
}

func writeDocumentExport(ctx context.Context, doc exportDocument, output string, video bool, options render.Options) error {
	comp, err := documentComposition(doc, video)
	if err != nil {
		return err
	}
	extension := ".png"
	if video {
		extension = ".mp4"
	}
	if !strings.EqualFold(filepath.Ext(output), extension) {
		return fmt.Errorf("Choose a %s file for this export.", extension)
	}
	// Encoders create additional files with their own permissions. Keep every
	// intermediate inside a private directory on the destination filesystem.
	stage, err := os.MkdirTemp(filepath.Dir(output), ".buddy-export-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	staged := filepath.Join(stage, "export"+extension)
	if video {
		err = render.Video(ctx, comp, staged, options)
	} else {
		file, openErr := os.OpenFile(staged, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if openErr != nil {
			return openErr
		}
		err = render.Frames(ctx, comp, options, func(_ int, pixels *image.RGBA) error { return png.Encode(file, pixels) })
		err = errors.Join(err, file.Close())
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		return err
	}
	if err = os.Chmod(staged, 0600); err != nil {
		return err
	}
	return os.Rename(staged, output)
}
