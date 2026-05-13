package evaluator

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"

	"github.com/incenva/rebate-scraper/db"
	"github.com/incenva/rebate-scraper/internal/llm"
	"github.com/incenva/rebate-scraper/models"
)

// TestCase is one entry in testdata/eval_testcases.json.
type TestCase struct {
	ID          string `json:"id"`
	Source      string `json:"source"`
	Description string `json:"description"`
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
}

// RunTestcases evaluates every entry in testdata/eval_testcases.json.
//
// For each URL it:
//  1. Fetches the page live
//  2. Sends content to GPT-4o for structured extraction
//  3. Looks up the matching staging row (by program_url)
//  4. Diffs LLM output vs staged data (or reports "no staged row" if the
//     scraper has never stored this URL — showing what it should capture)
func RunTestcases(cfg Config, filter string) ([]EvalResult, error) {
	cases, err := loadTestcases()
	if err != nil {
		return nil, fmt.Errorf("load testcases: %w", err)
	}

	client := llm.NewClient(cfg.OpenAIKey)
	var results []EvalResult

	for i, tc := range cases {
		if filter != "" && tc.Source != filter {
			continue
		}
		log.Printf("[%d/%d] testcase %s  url=%s", i+1, len(cases), tc.ID, tc.URL)

		res := EvalResult{
			Source:      tc.Source,
			ProgramName: tc.Description,
			SourceID:    tc.ID,
			ContentType: tc.ContentType,
		}

		body, ct, err := fetchURL(tc.URL, tc.ContentType)
		if err != nil {
			res.Error = err.Error()
			results = append(results, res)
			continue
		}
		res.ContentType = ct

		ext, err := client.ExtractIncentive(body, ct)
		if err != nil {
			res.Error = fmt.Sprintf("LLM extraction failed: %v", err)
			results = append(results, res)
			continue
		}

		// Update program name from what LLM extracted (more accurate than test case description)
		if ext.ProgramName != "" {
			res.ProgramName = ext.ProgramName
		}

		// Try to find a matching staging row.
		staged, found := lookupByURL(cfg.DB, tc.URL)
		if !found {
			// No staged row — show LLM-only extraction so user can see what the scraper should capture.
			res.FieldScores = llmOnlyScores(ext)
			res.OverallScore = 0
			res.MissingFields = allExtractedFields(ext)
			res.Error = "no staged row — scraper has not stored this URL yet"
			results = append(results, res)
			continue
		}

		scores := DiffFields(*staged, ext)
		res.OverallScore = OverallScore(scores)
		res.FieldScores = scores
		res.MissingFields = MissingFields(scores)
		results = append(results, res)
	}

	return results, nil
}

// loadTestcases reads testdata/eval_testcases.json relative to the project root.
func loadTestcases() ([]TestCase, error) {
	// Walk up from this source file to the project root (two dirs up from internal/evaluator).
	_, thisFile, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(thisFile), "..", "..", "testdata", "eval_testcases.json")

	// Also try CWD-relative path (works when built as a binary).
	if _, err := os.Stat(root); os.IsNotExist(err) {
		root = filepath.Join("testdata", "eval_testcases.json")
	}

	f, err := os.Open(root)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", root, err)
	}
	defer f.Close()

	var cases []TestCase
	if err := json.NewDecoder(f).Decode(&cases); err != nil {
		return nil, fmt.Errorf("decode testcases: %w", err)
	}
	return cases, nil
}

// lookupByURL finds the most-recently-updated staging row whose program_url matches.
func lookupByURL(d *db.DB, url string) (*models.StagedRebate, bool) {
	var row models.StagedRebate
	err := d.GORM().
		Where("program_url = ? OR application_url = ?", url, url).
		Order("updated_at DESC").
		First(&row).Error
	if err != nil {
		return nil, false
	}
	return &row, true
}

// fetchURL fetches a URL and returns body + detected content type.
func fetchURL(url, hintContentType string) (string, string, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; IncenvaEvaluator/1.0)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/json,*/*")

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
		ct = hintContentType
	}
	if ct == "" {
		ct = "text/html"
	}
	return string(body), ct, nil
}

// llmOnlyScores builds FieldScore rows from LLM extraction when there is no staged row.
// Every extracted field is marked "missing" (scraper never stored it).
func llmOnlyScores(ext *llm.LLMExtraction) []FieldScore {
	type kv struct {
		name string
		val  string
	}
	fields := []kv{
		{"program_name", ext.ProgramName},
		{"utility_company", ext.UtilityCompany},
		{"incentive_description", ext.IncentiveDescription},
		{"incentive_format", ext.IncentiveFormat},
		{"state", ext.State},
		{"categories", joinSlice(ext.Categories)},
		{"customer_type", ext.CustomerType},
		{"start_date", ext.StartDate},
		{"end_date", ext.EndDate},
		{"application_url", ext.ApplicationURL},
		{"contact_email", ext.ContactEmail},
		{"contact_phone", ext.ContactPhone},
		{"eligibility_notes", ext.EligibilityNotes},
	}

	var out []FieldScore
	for _, f := range fields {
		if f.val == "" {
			continue
		}
		out = append(out, FieldScore{
			Name:     f.name,
			Status:   StatusMissing,
			LLMValue: trunc(f.val, 38),
			Weight:   1,
		})
	}
	if ext.IncentiveAmount != nil {
		out = append(out, FieldScore{Name: "incentive_amount", Status: StatusMissing, LLMValue: fmt.Sprintf("%.2f", *ext.IncentiveAmount), Weight: 1})
	}
	if ext.MaximumAmount != nil {
		out = append(out, FieldScore{Name: "maximum_amount", Status: StatusMissing, LLMValue: fmt.Sprintf("%.2f", *ext.MaximumAmount), Weight: 1})
	}
	return out
}

// allExtractedFields returns the names of fields the LLM found a value for.
func allExtractedFields(ext *llm.LLMExtraction) []string {
	var out []string
	if ext.ProgramName != "" {
		out = append(out, "program_name")
	}
	if ext.UtilityCompany != "" {
		out = append(out, "utility_company")
	}
	if ext.IncentiveDescription != "" {
		out = append(out, "incentive_description")
	}
	if ext.IncentiveAmount != nil {
		out = append(out, "incentive_amount")
	}
	if ext.MaximumAmount != nil {
		out = append(out, "maximum_amount")
	}
	if ext.StartDate != "" {
		out = append(out, "start_date")
	}
	if ext.EndDate != "" {
		out = append(out, "end_date")
	}
	if len(ext.Categories) > 0 {
		out = append(out, "categories")
	}
	if ext.CustomerType != "" {
		out = append(out, "customer_type")
	}
	if ext.ContactPhone != "" {
		out = append(out, "contact_phone")
	}
	if ext.ContactEmail != "" {
		out = append(out, "contact_email")
	}
	if ext.EligibilityNotes != "" {
		out = append(out, "eligibility_notes")
	}
	return out
}

func joinSlice(s []string) string {
	result := ""
	for i, v := range s {
		if i > 0 {
			result += ", "
		}
		result += v
	}
	return result
}
