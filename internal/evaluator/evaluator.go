// Package evaluator compares staged scraper data against GPT-4o extractions
// to identify fields the scraper is missing or getting wrong.
package evaluator

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/incenva/rebate-scraper/db"
	"github.com/incenva/rebate-scraper/internal/llm"
	"github.com/incenva/rebate-scraper/models"
	"github.com/incenva/rebate-scraper/scrapers"
)

var fetchClient = &http.Client{Timeout: 15 * time.Second}

// apiSources are scrapers whose raw response is an API JSON blob, not an HTML
// page. For these, we re-use the cached stg_raw_response rather than fetching
// the program URL (which points to the public website, not the API endpoint).
var apiSources = map[string]bool{
	"dsireusa":    true,
	"Energy Star": true,
	"energy_star": true,
}

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
	Source        string            `json:"source"`
	ProgramName   string            `json:"program_name"`
	SourceID      string            `json:"source_id"`
	ProgramURL    string            `json:"program_url,omitempty"`
	SourceURL     string            `json:"source_url,omitempty"` // URL in the originating data source (DSIRE page, ES listing, etc.)
	ContentType   string            `json:"content_type"`
	OverallScore  float64           `json:"overall_score"`
	FieldScores   []FieldScore      `json:"field_scores"`
	MissingFields []string          `json:"missing_fields"`
	DBValues      map[string]string `json:"db_values,omitempty"`
	Error         string            `json:"error,omitempty"`
}

// Run fetches a sample of staging rows, sends each program's content to GPT-4o,
// re-runs the current scraper extraction on the same content, then diffs the two.
//
// For HTML-based scrapers the program URL is always fetched live so the
// evaluation reflects both current page content and current extraction code.
// For API-based scrapers (dsireusa, energy_star) the cached stg_raw_response is
// re-used (the live API is not easily re-callable by URL alone) but the
// extraction logic is re-run so code fixes are reflected immediately.
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
		// Throttle to avoid hitting the OpenAI TPM rate limit (30K tokens/min).
		// Each request uses ~2400 tokens → max ~12/min → 5s minimum spacing.
		if i > 0 {
			time.Sleep(5 * time.Second)
		}

		log.Printf("[%d/%d] evaluating: %q  (source=%s)", i+1, len(rows), row.ProgramName, row.Source)

		// Resolve the best URL to show for visual inspection.
		programURL := ""
		if row.ProgramURL != nil && *row.ProgramURL != "" {
			programURL = *row.ProgramURL
		} else if row.ApplicationURL != nil && *row.ApplicationURL != "" {
			programURL = *row.ApplicationURL
		}

		res := EvalResult{
			Source:      row.Source,
			ProgramName: row.ProgramName,
			SourceID:    row.SourceID,
			ProgramURL:  programURL,
			SourceURL:   resolveSourceURL(row),
			DBValues:    stagedToDBValues(row),
		}

		// Resolve content:
		// - API-based scrapers: use cached JSON (re-extracting is sufficient).
		// - HTML scrapers: always fetch live so the eval reflects current page content.
		rawContent, ct, fetchErr := resolveContentFresh(row)
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
			log.Printf("[DEBUG] content for %q (%s, %d bytes):\n%s\n",
				row.ProgramName, ct, len(rawContent), preview)
		}

		// LLM extraction.
		ext, err := client.ExtractIncentive(rawContent, ct)
		if err != nil {
			res.Error = fmt.Sprintf("LLM extraction failed: %v", err)
			results = append(results, res)
			continue
		}

		// Fresh scraper extraction — re-runs current code on the same content.
		pageURL := ""
		if row.ProgramURL != nil {
			pageURL = *row.ProgramURL
		}
		fresh := scrapers.Reextract(row.Source, rawContent, ct, pageURL, row.ScraperVersion, nil)

		var scores []FieldScore
		if fresh != nil {
			scores = DiffFieldsFresh(fresh, ext)
		} else {
			// Fallback: diff against DB row when re-extraction is unsupported.
			scores = DiffFields(row, ext)
		}
		res.FieldScores = scores
		res.OverallScore = OverallScore(scores)
		res.MissingFields = MissingFields(scores)

		results = append(results, res)
	}

	return results, nil
}

// resolveContentFresh returns the raw content to evaluate.
// HTML scrapers always fetch live; API-based scrapers re-use the cached response.
func resolveContentFresh(row models.StagedRebate) (content, contentType string, err error) {
	if apiSources[row.Source] {
		// API-based: cached response IS the API payload — use it.
		if row.StgRawResponse != nil && *row.StgRawResponse != "" {
			ct := "application/json"
			if row.StgRawContentType != nil && *row.StgRawContentType != "" {
				ct = *row.StgRawContentType
			}
			return *row.StgRawResponse, ct, nil
		}
	}

	// HTML-based (or API-based with no cache): fetch the program URL live.
	url := ""
	if row.ProgramURL != nil && *row.ProgramURL != "" {
		url = *row.ProgramURL
	} else if row.ApplicationURL != nil && *row.ApplicationURL != "" {
		url = *row.ApplicationURL
	}
	if url == "" {
		return "", "", fmt.Errorf("no URL to fetch for %q", row.ProgramName)
	}

	log.Printf("  → fetching live: %s", url)
	return fetchLiveURL(url)
}

// fetchLiveURL fetches a URL and returns its body and detected content type.
func fetchLiveURL(url string) (string, string, error) {
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

	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return "", "", fmt.Errorf("read body: %w", err)
	}

	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "text/html"
	}
	return string(body), ct, nil
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
			Where("source = ? AND program_url IS NOT NULL AND program_url != '' AND program_url NOT LIKE '%/login%' AND program_url NOT LIKE '%clearesult.com%'", src).
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

// resolveSourceURL returns the canonical URL in the originating data system for
// the given staged row. Uses the stored source_url column when available (set by
// current scraper versions); falls back to constructing it from the raw response
// for older rows that pre-date the column.
func resolveSourceURL(row models.StagedRebate) string {
	// Prefer the persisted DB value — most accurate and fastest path.
	if row.SourceURL != nil && *row.SourceURL != "" {
		return *row.SourceURL
	}

	// Fallback: reconstruct from raw response for rows scraped before source_url existed.
	switch row.Source {
	case "dsireusa":
		if row.StgRawResponse != nil && *row.StgRawResponse != "" {
			var obj struct {
				ID int `json:"id"`
			}
			if err := json.Unmarshal([]byte(*row.StgRawResponse), &obj); err == nil && obj.ID > 0 {
				return fmt.Sprintf("https://programs.dsireusa.org/system/program/detail/%d", obj.ID)
			}
		}
	case "Energy Star", "energy_star":
		if row.StgRawResponse != nil && *row.StgRawResponse != "" {
			var obj struct {
				IncentiveID string `json:"incentive_id"`
			}
			if err := json.Unmarshal([]byte(*row.StgRawResponse), &obj); err == nil && obj.IncentiveID != "" {
				return fmt.Sprintf("https://www.energystar.gov/rebate-finder?incentive_id=%s", obj.IncentiveID)
			}
		}
	case "rewiring_america":
		// RA stores the program_url in the raw response item.
		if row.StgRawResponse != nil && *row.StgRawResponse != "" {
			var enriched struct {
				Item struct {
					ProgramURL  string `json:"program_url"`
					MoreInfoURL string `json:"more_info_url"`
				} `json:"item"`
			}
			if err := json.Unmarshal([]byte(*row.StgRawResponse), &enriched); err == nil {
				if enriched.Item.ProgramURL != "" {
					return enriched.Item.ProgramURL
				}
				if enriched.Item.MoreInfoURL != "" {
					return enriched.Item.MoreInfoURL
				}
			}
			// Legacy format: raw raIncentive.
			var item struct {
				ProgramURL  string `json:"program_url"`
				MoreInfoURL string `json:"more_info_url"`
			}
			if err := json.Unmarshal([]byte(*row.StgRawResponse), &item); err == nil {
				if item.ProgramURL != "" {
					return item.ProgramURL
				}
				if item.MoreInfoURL != "" {
					return item.MoreInfoURL
				}
			}
		}
	}
	// For HTML scrapers and fallback: the program_url IS the scraped source URL.
	if row.ProgramURL != nil && *row.ProgramURL != "" {
		return *row.ProgramURL
	}
	return ""
}

// stagedToDBValues extracts every non-empty field from a StagedRebate into a
// flat string map so the report can show the full DB state side-by-side with
// the LLM extraction for visual comparison.
func stagedToDBValues(r models.StagedRebate) map[string]string {
	m := make(map[string]string)
	set := func(k string, v *string) {
		if v != nil && *v != "" {
			m[k] = *v
		}
	}
	setf := func(k string, v *float64) {
		if v != nil {
			m[k] = fmt.Sprintf("%g", *v)
		}
	}
	setb := func(k string, v *bool) {
		if v != nil {
			if *v {
				m[k] = "true"
			} else {
				m[k] = "false"
			}
		}
	}
	sets := func(k string, v []string) {
		if len(v) > 0 {
			m[k] = strings.Join(v, ", ")
		}
	}

	m["program_name"] = r.ProgramName
	m["utility_company"] = r.UtilityCompany
	m["source"] = r.Source
	m["source_id"] = r.SourceID
	m["scraper_version"] = r.ScraperVersion
	m["promotion_status"] = r.PromotionStatus
	set("incentive_description", r.IncentiveDescription)
	setf("incentive_amount", r.IncentiveAmount)
	setf("maximum_amount", r.MaximumAmount)
	setf("percent_value", r.PercentValue)
	setf("per_unit_amount", r.PerUnitAmount)
	set("unit_type", r.UnitType)
	set("incentive_format", r.IncentiveFormat)
	set("state", r.State)
	set("zip_code", r.ZipCode)
	set("service_territory", r.ServiceTerritory)
	setb("available_nationwide", r.AvailableNationwide)
	sets("categories", []string(r.CategoryTag))
	sets("portfolio", []string(r.Portfolio))
	set("implementing_sector", r.ImplementingSector)
	sets("segment", []string(r.Segment))
	set("customer_type", r.CustomerType)
	set("product_category", r.ProductCategory)
	set("administrator", r.Administrator)
	set("start_date", r.StartDate)
	set("end_date", r.EndDate)
	setb("while_funds_last", r.WhileFundsLast)
	set("source_url", r.SourceURL)
	set("program_url", r.ProgramURL)
	set("application_url", r.ApplicationURL)
	set("application_process", r.ApplicationProcess)
	set("contact_email", r.ContactEmail)
	set("contact_phone", r.ContactPhone)
	setb("contractor_required", r.ContractorRequired)
	setb("energy_audit_required", r.EnergyAuditRequired)
	if r.PromotedAt != nil {
		m["promoted_at"] = r.PromotedAt.Format("2006-01-02 15:04:05")
	}
	if r.RebateID != nil && *r.RebateID != "" {
		m["rebate_id"] = *r.RebateID
	}
	return m
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

