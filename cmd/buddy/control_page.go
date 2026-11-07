package main

import "github.com/egoist/mygo/ui"

const controlPageInset = 24
const controlPageBottom = 20

func controlPageLayout(c *ui.Context, title, subtitle string, content func()) {
	ui.Column(c).Grow(1).MinHeight(0).MinWidth(0).FillWidth().Padding(18, 0, 0).Gap(14).Children(func() {
		ui.Column(c).FillWidth().Padding(0, controlPageInset).Gap(4).Children(func() {
			ui.Text(c, title).Role(ui.RoleHeading).FontSize(29).FontWeight(700).LetterSpacing(-.7)
			ui.Text(c, subtitle).FontSize(13).TextColor(c.Theme().TextMuted)
		})
		content()
	})
}

func controlPage(c *ui.Context, title, subtitle string, controls, body func()) {
	controlPageLayout(c, title, subtitle, func() {
		if controls != nil {
			ui.Column(c).FillWidth().Padding(0, controlPageInset).Children(controls)
		}
		controlPageScroll(c.Key("control-body"), body)
	})
}

func controlPageScroll(c *ui.Context, body func()) {
	// Put the page gutters inside the scroll area so its scrollbar stays outside the content.
	ui.Scroll(c).Grow(1).MinHeight(0).FillWidth().Children(func() {
		ui.Column(c).FillWidth().MinWidth(0).Padding(0, controlPageInset, controlPageBottom).Gap(12).Children(body)
	})
}

func controlSection(c *ui.Context, title, description string, action func()) {
	ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
			ui.Text(c, title).Role(ui.RoleHeading).FontSize(16).FontWeight(680)
			if description != "" {
				ui.Text(c, description).FontSize(12).TextColor(c.Theme().TextMuted)
			}
		})
		if action != nil {
			action()
		}
	})
}

func quietControl(c *ui.Context, title string, icon *ui.SVG, enabled bool, action func()) {
	btn := ui.ButtonBase(c).Label(title).Height(29).Shrink(0).Padding(0, 8).Gap(5).
		Radius(6).Disabled(!enabled).OnClick(action)
	if btn.Hovered() {
		btn.Background(c.Theme().SurfacePressed)
	}
	btn.Children(func() {
		if icon != nil {
			ui.Icon(c, icon).Size(14, 14).Shrink(0).TextColor(c.Theme().TextMuted)
		}
		ui.Text(c, title).SingleLine().FontSize(12).FontWeight(600)
	})
}

func controlTab(c *ui.Context, label string, icon *ui.SVG, selected bool, action func()) {
	t := c.Theme()
	color := t.TextMuted
	btn := ui.ButtonBase(c).Label(label).Selected(selected).Height(35).Padding(0, 10).Gap(6).
		Radius(5).OnClick(action)
	if selected || btn.Hovered() {
		color = t.Text
	}
	if btn.Hovered() {
		btn.Background(t.SurfaceHover)
	} else if selected {
		btn.Background(t.Background)
	}
	if selected {
		btn.BorderWidth(0, 0, 2, 0).BorderColor(t.Accent)
	}
	btn.Children(func() {
		if icon != nil {
			ui.Icon(c, icon).Size(14, 14).Shrink(0).TextColor(color)
		}
		ui.Text(c, label).SingleLine().FontSize(12).FontWeight(640).TextColor(color)
	})
}

func controlEmpty(c *ui.Context, icon *ui.SVG, title, description string) {
	connectionSection(c, func() {
		ui.Row(c).FillWidth().Padding(15).Gap(11).AlignItems(ui.Center).Children(func() {
			connectionDetail(c, icon, title, description)
		})
	})
}
