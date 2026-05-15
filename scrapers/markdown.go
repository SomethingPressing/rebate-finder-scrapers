package scrapers

import (
	"strings"

	md "github.com/JohannesKaufmann/html-to-markdown"
	"github.com/PuerkitoBio/goquery"
	"github.com/gocolly/colly/v2"
)

var mdConverter = func() *md.Converter {
	c := md.NewConverter("", true, nil)
	// Strip images — they produce noise like "![](...)" in plain-text contexts.
	c.AddRules(md.Rule{
		Filter: []string{"img"},
		Replacement: func(_ string, _ *goquery.Selection, _ *md.Options) *string {
			s := ""
			return &s
		},
	})
	return c
}()

// HTMLToMarkdown converts an HTML fragment to clean Markdown.
// Lists, bold, italic, and links are preserved; images are stripped.
// Falls back to plain-text tag-stripping if conversion fails.
func HTMLToMarkdown(rawHTML string) string {
	if rawHTML == "" {
		return ""
	}
	result, err := mdConverter.ConvertString(rawHTML)
	if err != nil {
		return stripHTML(rawHTML)
	}
	for strings.Contains(result, "\n\n\n") {
		result = strings.ReplaceAll(result, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(result)
}

// CollyDescriptionMarkdown extracts the incentive description from a Colly
// HTMLElement as Markdown.  It collects the inner HTML of the first substantive
// paragraphs (≥60 chars of text), converts them, and truncates to maxLen runes.
// Falls back to the meta description, then to fallbackName.
func CollyDescriptionMarkdown(e *colly.HTMLElement, fallbackName string, maxLen int) string {
	if maxLen <= 0 {
		maxLen = 1000
	}

	// Collect inner HTML of substantial paragraphs up to ~2 KB of raw HTML.
	var parts []string
	rawTotal := 0
	e.DOM.Find("p").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		text := strings.TrimSpace(s.Text())
		if len(text) >= 60 {
			if h, err := s.Html(); err == nil {
				parts = append(parts, "<p>"+h+"</p>")
				rawTotal += len(h)
			}
		}
		return rawTotal < 2000
	})

	if len(parts) > 0 {
		result := HTMLToMarkdown(strings.Join(parts, "\n"))
		if len([]rune(result)) > maxLen {
			runes := []rune(result)
			result = string(runes[:maxLen-3]) + "..."
		}
		if result != "" {
			return result
		}
	}

	// Fall back to meta description.
	if meta := strings.TrimSpace(e.ChildAttr(`meta[name="description"]`, "content")); meta != "" {
		if len(meta) > maxLen {
			meta = meta[:maxLen-3] + "..."
		}
		return meta
	}

	return fallbackName
}
