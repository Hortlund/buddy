package main

import (
	"net/url"
	"strings"

	"buddy/internal/tasks"
	"github.com/egoist/mygo/ui"
)

func githubCard(c *ui.Context, result *tasks.GitHubResult) {
	panel := ui.Column(c).MinWidth(0).MinHeight(64).Padding(10, 12).Gap(7).
		Background(c.Theme().Surface).Radius(9).Border(1, c.Theme().Border)
	showPatch := ui.Local(panel, "show-patch", func() bool { return false })
	services := c.Services()
	panel.Children(func() {
		ui.Text(c, "GitHub · "+result.Repo).FontSize(11).TextColor(c.Theme().TextMuted)
		ui.Text(c, result.Heading).Role(ui.RoleHeading).FontSize(14).FontWeight(650)
		if result.Detail != "" {
			ui.Text(c, result.Detail).FontSize(12).MaxLines(5).TextColor(c.Theme().TextMuted).Selectable()
		}
		for i, item := range result.Items {
			ui.Row(c.Key(i)).AlignItems(ui.Center).Gap(8).Padding(5, 0).
				BorderWidth(1, 0, 0, 0).BorderColor(c.Theme().Border).Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
					ui.Text(c, item.Title).FontSize(12).FontWeight(580).MaxLines(2)
					if item.Detail != "" {
						ui.Text(c, item.Detail).FontSize(11).TextColor(c.Theme().TextMuted).MaxLines(2)
					}
				})
				if validGitHubLink(item.URL) {
					ui.Button(c.Key("open-item"), "Open").Tooltip("Open " + item.Title + " on GitHub").
						OnClick(func() { services.OpenURL(item.URL) })
				}
			})
		}
		ui.Row(c).Gap(6).Wrap().Children(func() {
			if validGitHubLink(result.URL) {
				label := "Open on GitHub"
				if result.More {
					label = "See more on GitHub"
				}
				ui.Button(c.Key("github-link"), label).OnClick(func() { services.OpenURL(result.URL) })
			}
			if result.Patch != "" {
				label := "Show diff preview"
				if *showPatch {
					label = "Hide diff preview"
				}
				ui.Button(c.Key("github-patch"), label).OnClick(func() { *showPatch = !*showPatch })
			}
		})
		if *showPatch && result.Patch != "" {
			fence := strings.Repeat("`", 3)
			for strings.Contains(result.Patch, fence) {
				fence += "`"
			}
			messageMarkdown(c.Key("diff-preview"), fence+"diff\n"+result.Patch+"\n"+fence)
		}
	})
}

func validGitHubLink(link string) bool {
	u, err := url.Parse(link)
	return err == nil && u.Scheme == "https" && u.Host == "github.com" && u.User == nil
}
