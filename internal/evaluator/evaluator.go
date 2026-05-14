// Package evaluator compares staged scraper data against GPT-4o extractions
// to identify fields the scraper is missing or getting wrong.
package evaluator

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/incenva/rebate-scraper/db"
	"github.com/incenva/rebate-scraper/internal/llm"
	"github.com/incenva/rebate-scraper/models"
)

var fetchClient = &http.Client{Timeout: 15 * time.Second}

// Config controls the evaluation run.
type Config struct {
	DB           *db.DB
	OpenAIKey    string
	Source       string // restrict to one scraper source; empty = all sources
	SampleN      int    // rows to sample per source (default 2)
	OutputFormat string // "table" or "json"
	// Debug enables verbose output: raw content preview sent to the LLM and the
	// full LLM JSON response are printed to stderr for each evaluated program.
	Debug bool
}

// EvalResult is the comparison result for one staging row.
type EvalResult struct {
	Source        string       `json:"source"`
	ProgramName   string       `json:"program_name"`
	SourceID      string       `json:"source_id"`
	ContentType   string       `json:"content_type"`
	OverallScore  float64      `json:"overall_score"`
	FieldScores   []FieldScore `json:"field_scores"`
	MissingFields []string     `json:"missing_fields"`
	Error         string       `json:"error,omitempty"`
}

// Run fetches a sample of staging rows, sends each to GPT-4o, and returns per-row results.
func Run(cfg Config) ([]EvalResult, error) {
	rows, err := fetchSample(cfg.DB, cfg.Source, cfg.SampleN)
	if err != nil {
		return nil, fmt.Errorf("fetch sample: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("no staging rows found — run the scraper first")
	}

	client := llm.NewClient(cfg.OpenAIKey).WithDebug(cfg.Debug)
	results := make([]EvalResult, 0, len(rows))

	for i, row := range rows {
		log.Printf("[%d/%d] evaluating: %q  (source=%s)", i+1, len(rows), row.ProgramName, row.Source)

		res := EvalResult{
			Source:      row.Source,
			ProgramName: row.ProgramName,
			SourceID:    row.SourceID,
		}

		rawContent, ct, fetchErr := resolveContent(row)
		if fetchErr != nil {
			res.Error = fetchErr.Error()
			results = append(results, res)
			continue
		}
		res.ContentType = ct

		if cfg.Debug {
			preview := rawContent
			if len(preview) > 1000 {
				preview = preview[:1000] + fmt.Sprintf("\n... [%d more bytes]", len(rawContent)-1000)
			}
			log.Printf("[DEBUG] resolved content for %q (%s, %d bytes):\n%s\n",
				row.ProgramName, ct, len(rawContent), preview)
		}

		ext, err := client.ExtractIncentive(rawContent, ct)
		if err != nil {
			res.Error = fmt.Sprintf("LLM extraction failed: %v", err)
			results = append(results, res)
			continue
		}

		scores := DiffFields(row, ext)
		res.FieldScores = scores
		res.OverallScore = OverallScore(scores)
		res.MissingFields = MissingFields(scores)

		results = append(results, res)
	}

	return results, nil
}

// junkNamePatterns are program_name substrings that indicate a non-incentive page
// was scraped (login walls, notice pages, feedback forms, nav concatenations).
var junkNamePatterns = []string{
	"log in", "sign in", "extra verification", "access denied",
	"we welcome your feedback", "page not found", "404",
	"customer notice", "session expired",
}

// fetchSample returns up to n quality-filtered rows per source.
// Prefers rows with stg_raw_response; falls back to any row with a program_url.
// Rows whose program_name matches known junk patterns are excluded.
func fetchSample(d *db.DB, source string, n int) ([]models.StagedRebate, error) {
	var sources []string
	q := d.GORM().Model(&models.StagedRebate{}).
		Select("DISTINCT source").
		Where("program_url IS NOT NULL AND program_url != ''")
	if source != "" {
		q = q.Where("source = ?", source)
	}
	if err := q.Pluck("source", &sources).Error; err != nil {
		return nil, fmt.Errorf("list sources: %w", err)
	}
	if len(sources) == 0 {
		return nil, nil
	}

	var all []models.StagedRebate
	for _, src := range sources {
		var candidates []models.StagedRebate
		// Fetch more than n so we have room to filter out junk rows.
		err := d.GORM().
			Where("source = ? AND program_url IS NOT NULL AND program_url != '' AND program_url NOT LIKE '%/login%'", src).
			Order("(stg_raw_response IS NOT NULL) DESC, updated_at DESC").
			Limit(n * 5).
			Find(&candidates).Error
		if err != nil {
			return nil, fmt.Errorf("fetch rows for source %q: %w", src, err)
		}

		added := 0
		for _, row := range candidates {
			if added >= n {
				break
			}
			if isJunkRow(row) {
				continue
			}
			all = append(all, row)
			added++
		}
	}
	return all, nil
}

// isJunkRow returns true when a staged row looks like it was scraped from a
// non-incentive page (login wall, notice, feedback form, etc.).
func isJunkRow(row models.StagedRebate) bool {
	lower := strings.ToLower(row.ProgramName)
	for _, p := range junkNamePatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	// Reject very short program names (< 10 chars) — almost certainly noise.
	if len(strings.TrimSpace(row.ProgramName)) < 10 {
		return true
	}
	return false
}

// resolveContent returns the raw content and content-type for a staged row.
// Uses the stored stg_raw_response if available, otherwise fetches program_url.
func resolveContent(row models.StagedRebate) (content, contentType string, err error) {
	if row.StgRawResponse != nil && *row.StgRawResponse != "" {
		ct := "text/html"
		if row.StgRawContentType != nil && *row.StgRawContentType != "" {
			ct = *row.StgRawContentType
		}
		return *row.StgRawResponse, ct, nil
	}

	// Fall back: fetch the program URL live.
	url := ""
	if row.ProgramURL != nil && *row.ProgramURL != "" {
		url = *row.ProgramURL
	} else if row.ApplicationURL != nil && *row.ApplicationURL != "" {
		url = *row.ApplicationURL
	}
	if url == "" {
		return "", "", fmt.Errorf("no raw response and no program_url to fetch")
	}

	log.Printf("  → no stored response; fetching %s", url)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; IncenvaEvaluator/1.0)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/json")

	resp, err := fetchClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return "", "", fmt.Errorf("fetch %s returned HTTP %d", url, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024)) // cap at 512 KB
	if err != nil {
		return "", "", fmt.Errorf("read body: %w", err)
	}

	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "text/html"
	}
	return string(body), ct, nil
}
