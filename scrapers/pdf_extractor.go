// pdf_extractor.go — generic helpers for extracting plain text from PDF files.
//
// Supports both local files and remote URLs:
//
//	// Local file
//	text, err := ExtractLocalPDFPages("/path/to/file.pdf",
//	    []PageRange{{Start: 10, End: 12}, {Start: 50, End: 50}})
//
//	// Remote URL (downloads to a temp file first)
//	text, err := ExtractPDFPages("https://example.com/file.pdf",
//	    []PageRange{{Start: 10, End: 12}})
package scrapers

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ledongthuc/pdf"
)

// PageRange specifies a contiguous range of pages (1-based, inclusive) to
// extract from a PDF.  Start == End extracts a single page.
type PageRange struct {
	Start int
	End   int
}

// ExtractLocalPDFPages opens a local PDF file and extracts plain text from
// every page in ranges.  Pages outside the PDF's actual page count are clamped.
func ExtractLocalPDFPages(filePath string, ranges []PageRange) (string, error) {
	return extractPages(filePath, ranges)
}

// ExtractPDFPages downloads the PDF at url into a temporary file, extracts
// plain text from every page in ranges, and returns the concatenated text.
// Pages outside the PDF's actual page count are silently clamped.
//
// The temporary file is removed before the function returns.
func ExtractPDFPages(url string, ranges []PageRange) (string, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Incenva-Scraper/1.0)")
	req.Header.Set("Accept", "application/pdf,*/*")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download PDF %q: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("download PDF %q: HTTP %d", url, resp.StatusCode)
	}

	tmp, err := os.CreateTemp("", "incenva-pdf-*.pdf")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		return "", fmt.Errorf("write temp file: %w", err)
	}
	tmp.Close()

	return extractPages(tmpName, ranges)
}

// extractPages is the shared core: open a PDF by path and pull text from ranges.
func extractPages(filePath string, ranges []PageRange) (string, error) {
	f, r, err := pdf.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("parse PDF %q: %w", filePath, err)
	}
	defer f.Close()

	totalPages := r.NumPage()

	var sb strings.Builder
	for _, rng := range ranges {
		end := rng.End
		if end > totalPages {
			end = totalPages
		}
		for pageNum := rng.Start; pageNum <= end; pageNum++ {
			p := r.Page(pageNum)
			if p.V.IsNull() {
				continue
			}
			text, err := p.GetPlainText(nil)
			if err != nil {
				continue
			}
			if sb.Len() > 0 {
				sb.WriteString("\n\n--- page break ---\n\n")
			}
			sb.WriteString(text)
		}
	}

	return sb.String(), nil
}
