package automations

import (
	"encoding/base64"
	"io"
	"mime"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

var mailWordDecoder = mime.WordDecoder{CharsetReader: charset.NewReaderLabel}

func decodeMailHeader(value string) string {
	decoded, err := mailWordDecoder.DecodeHeader(value)
	if err != nil {
		return value
	}
	return decoded
}

func gmailPartText(part gmailChatPart, kind string, depth int) string {
	if depth > 12 || part.Name != "" || part.Body.Attachment != "" || part.MIME == "message/rfc822" {
		return ""
	}
	if part.MIME == kind && part.Body.Data != "" {
		prefix := part.Body.Data[:min(len(part.Body.Data), 48000)]
		data, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(prefix, "="))
		if err != nil {
			return ""
		}
		text := string(data)
		for _, header := range part.Headers {
			if strings.EqualFold(header.Name, "Content-Type") {
				_, params, err := mime.ParseMediaType(header.Value)
				if label := params["charset"]; err == nil && label != "" {
					reader, err := charset.NewReaderLabel(label, strings.NewReader(text))
					if err == nil {
						if decoded, err := io.ReadAll(io.LimitReader(reader, 64<<10)); err == nil {
							text = string(decoded)
						}
					}
				}
				break
			}
		}
		if kind == "text/html" {
			text = gmailHTMLText(text)
		}
		return clipGitHubText(strings.ToValidUTF8(text, "�"), 4000)
	}
	for _, child := range part.Parts {
		if body := gmailPartText(child, kind, depth+1); body != "" {
			return body
		}
	}
	return ""
}

func gmailHTMLText(text string) string {
	tokenizer := html.NewTokenizer(strings.NewReader(text))
	var result strings.Builder
	var hidden string
	for {
		kind := tokenizer.Next()
		switch kind {
		case html.ErrorToken:
			var lines []string
			for line := range strings.SplitSeq(result.String(), "\n") {
				if trimmed := strings.TrimSpace(line); trimmed != "" {
					lines = append(lines, trimmed)
				}
			}
			return strings.Join(lines, "\n")
		case html.TextToken:
			if hidden == "" {
				result.WriteString(strings.Join(strings.Fields(string(tokenizer.Text())), " "))
				result.WriteByte(' ')
			}
		case html.StartTagToken, html.EndTagToken, html.SelfClosingTagToken:
			name, _ := tokenizer.TagName()
			tag := string(name)
			if hidden != "" {
				if kind == html.EndTagToken && tag == hidden {
					hidden = ""
				}
				continue
			}
			switch tag {
			case "head", "script", "style", "template":
				if kind == html.StartTagToken {
					hidden = tag
				}
			case "br", "p", "div", "li", "tr", "h1", "h2", "h3", "h4", "blockquote":
				if result.Len() != 0 {
					result.WriteByte('\n')
				}
			}
		}
	}
}
