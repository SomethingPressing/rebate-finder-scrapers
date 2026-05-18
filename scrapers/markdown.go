package scrapers

import (
	"strings"

	md "github.com/JohannesKaufmann/html-to-markdown"
	"github.com/PuerkitoBio/goquery"
	"github.com/gocolly/colly/v2"
)

// descJunkPhrases — lower-case substrings that, when found in a paragraph's
// text, mark it as browser-detection noise or error boilerplate rather than
// real programme content.  Checked before the paragraph is added to the
// description.
var descJunkPhrases = []string{
	"unsupported browser",
	"please download the latest version of chrome",
	"please download the latest version of firefox",
	"download the latest version of chrome, firefox",
	"error: please call us at",
}

func isJunkParagraph(text string) bool {
	lower := strings.ToLower(text)
	for _, phrase := range descJunkPhrases {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

// imageSkipKeywords — src substrings that indicate a logo, icon or UI asset
// rather than a real program image. Checked case-insensitively.
var imageSkipKeywords = []string{
	"logo", "icon", "favicon", "sprite", "arrow", "chevron",
	"button", "badge", "avatar", "placeholder", "tracking", "pixel",
	".svg", "data:image",
}

func looksLikeRealImage(src string) bool {
	if src == "" {
		return false
	}
	low := strings.ToLower(src)
	for _, kw := range imageSkipKeywords {
		if strings.Contains(low, kw) {
			return false
		}
	}
	return true
}

// resolveImageSrc converts a raw img src to an absolute URL, or returns ""
// if the src is not a usable image URL.
func resolveImageSrc(src, baseURL string) string {
	switch {
	case strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://"):
		return src // already absolute — never prepend base
	case strings.HasPrefix(src, "//"):
		return "https:" + src
	case strings.HasPrefix(src, "/") && baseURL != "":
		return strings.TrimRight(baseURL, "/") + src
	default:
		return "" // relative path without base, or data: URI — skip
	}
}

// extractImageURL returns the best image URL for a scraped page.
// Priority: og:image → twitter:image → first <img> whose src looks like a
// real content image (not a logo/icon/svg).
func extractImageURL(doc *goquery.Document, baseURL string) string {
	// og:image
	if og, exists := doc.Find(`meta[property="og:image"]`).Attr("content"); exists && og != "" {
		return og
	}
	// twitter:image
	if tw, exists := doc.Find(`meta[name="twitter:image"]`).Attr("content"); exists && tw != "" {
		return tw
	}
	// First content <img>
	var found string
	doc.Find("img[src]").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		src, _ := s.Attr("src")
		if !looksLikeRealImage(src) {
			return true
		}
		if abs := resolveImageSrc(src, baseURL); abs != "" {
			found = abs
			return false
		}
		return true
	})
	return found
}

// CollyImageURL returns the best image URL from a Colly HTMLElement.
func CollyImageURL(e *colly.HTMLElement, baseURL string) string {
	// og:image
	if og := e.ChildAttr(`meta[property="og:image"]`, "content"); og != "" {
		return og
	}
	// twitter:image
	if tw := e.ChildAttr(`meta[name="twitter:image"]`, "content"); tw != "" {
		return tw
	}
	// First content <img>
	var found string
	e.DOM.Find("img[src]").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		src, _ := s.Attr("src")
		if !looksLikeRealImage(src) {
			return true
		}
		if abs := resolveImageSrc(src, baseURL); abs != "" {
			found = abs
			return false
		}
		return true
	})
	return found
}

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
		// Skip paragraphs inside <noscript> — they're browser-detection fallbacks.
		if s.Closest("noscript").Length() > 0 {
			return true
		}
		text := strings.TrimSpace(s.Text())
		if len(text) >= 60 && !isJunkParagraph(text) {
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
