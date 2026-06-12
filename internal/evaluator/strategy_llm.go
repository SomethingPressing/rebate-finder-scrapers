package evaluator

import (
	"fmt"
	"log"

	"github.com/incenva/rebate-scraper/internal/llm"
	"github.com/incenva/rebate-scraper/models"
	"github.com/incenva/rebate-scraper/scrapers"
)

// htmlEvaluator handles plain-HTML sources.
// It re-fetches the live program URL via HTTP, sends the content to GPT-4o,
// re-runs the scraper's own extraction logic on the same content, then diffs
// the two results field by field.
type htmlEvaluator struct{}

func (e *htmlEvaluator) Mode() string   { return "" }
func (e *htmlEvaluator) UsesLLM() bool { return true }

func (e *htmlEvaluator) EvaluateDB(cfg Config, row models.StagedRebate) EvalResult {
	res := buildBaseResult(row)

	url := ""
	if row.ProgramURL != nil && *row.ProgramURL != "" {
		url = *row.ProgramURL
	} else if row.ApplicationURL != nil && *row.ApplicationURL != "" {
		url = *row.ApplicationURL
	}
	if url == "" {
		res.Error = fmt.Sprintf("no URL to fetch for %q", row.ProgramName)
		return res
	}

	log.Printf("  → fetching live: %s", url)
	rawContent, ct, err := fetchLiveURL(url)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.ContentType = ct

	if cfg.Debug {
		logContentPreview(row.ProgramName, ct, rawContent)
	}

	client := llm.NewClient(cfg.OpenAIKey).WithDebug(cfg.Debug)
	ext, err := client.ExtractIncentive(rawContent, ct)
	if err != nil {
		res.Error = fmt.Sprintf("LLM extraction failed: %v", err)
		return res
	}

	pageURL := ""
	if row.ProgramURL != nil {
		pageURL = *row.ProgramURL
	}
	fresh := scrapers.Reextract(row.Source, rawContent, ct, pageURL, row.ScraperVersion, nil)

	var scores []FieldScore
	if fresh != nil {
		scores = DiffFieldsFresh(fresh, ext)
	} else {
		scores = DiffFields(row, ext)
	}
	res.FieldScores = scores
	res.OverallScore = OverallScore(scores)
	res.MissingFields = MissingFields(scores)
	return res
}

func (e *htmlEvaluator) EvaluateTestcase(cfg Config, tc TestCase, staged *models.StagedRebate) EvalResult {
	return evaluateTestcaseWithLLM(cfg, tc, staged)
}

// cachedEvaluator handles API-based sources (dsireusa, energy_star) whose raw
// responses are stored as JSON blobs in stg_raw_response.  It uses the cached
// response for DB evaluation so the live API endpoint does not need to be
// re-called; for test cases it falls back to a live HTTP fetch.
type cachedEvaluator struct{}

func (e *cachedEvaluator) Mode() string   { return "" }
func (e *cachedEvaluator) UsesLLM() bool { return true }

func (e *cachedEvaluator) EvaluateDB(cfg Config, row models.StagedRebate) EvalResult {
	res := buildBaseResult(row)

	rawContent, ct, err := resolveCache(row)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.ContentType = ct

	if cfg.Debug {
		logContentPreview(row.ProgramName, ct, rawContent)
	}

	client := llm.NewClient(cfg.OpenAIKey).WithDebug(cfg.Debug)
	ext, err := client.ExtractIncentive(rawContent, ct)
	if err != nil {
		res.Error = fmt.Sprintf("LLM extraction failed: %v", err)
		return res
	}

	pageURL := ""
	if row.ProgramURL != nil {
		pageURL = *row.ProgramURL
	}
	fresh := scrapers.Reextract(row.Source, rawContent, ct, pageURL, row.ScraperVersion, nil)

	var scores []FieldScore
	if fresh != nil {
		scores = DiffFieldsFresh(fresh, ext)
	} else {
		scores = DiffFields(row, ext)
	}
	res.FieldScores = scores
	res.OverallScore = OverallScore(scores)
	res.MissingFields = MissingFields(scores)
	return res
}

func (e *cachedEvaluator) EvaluateTestcase(cfg Config, tc TestCase, staged *models.StagedRebate) EvalResult {
	// No cached response available for test-case URLs; use live HTTP + LLM.
	return evaluateTestcaseWithLLM(cfg, tc, staged)
}

// ── shared LLM testcase helper ────────────────────────────────────────────────

// evaluateTestcaseWithLLM fetches the test-case URL, sends content to GPT-4o,
// and diffs the result against the staged row (or shows LLM-only extraction
// when no staged row exists yet).
func evaluateTestcaseWithLLM(cfg Config, tc TestCase, staged *models.StagedRebate) EvalResult {
	res := buildBaseTestcaseResult(tc)

	body, ct, err := fetchURL(tc.URL, tc.ContentType)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.ContentType = ct

	if cfg.Debug {
		logContentPreview(tc.Description, ct, body)
	}

	client := llm.NewClient(cfg.OpenAIKey).WithDebug(cfg.Debug)
	ext, err := client.ExtractIncentive(body, ct)
	if err != nil {
		res.Error = fmt.Sprintf("LLM extraction failed: %v", err)
		return res
	}

	if ext.ProgramName != "" {
		res.ProgramName = ext.ProgramName
	}

	if staged == nil {
		res.FieldScores = llmOnlyScores(ext)
		res.OverallScore = 0
		res.MissingFields = allExtractedFields(ext)
		res.Error = "no staged row — scraper has not stored this URL yet"
		return res
	}

	scores := DiffFields(*staged, ext)
	res.OverallScore = OverallScore(scores)
	res.FieldScores = scores
	res.MissingFields = MissingFields(scores)
	return res
}

// logContentPreview prints a truncated content snippet to stderr in debug mode.
func logContentPreview(name, ct, content string) {
	preview := content
	if len(preview) > 1000 {
		preview = preview[:1000] + fmt.Sprintf("\n... [%d more bytes]", len(content)-1000)
	}
	log.Printf("[DEBUG] content for %q (%s, %d bytes):\n%s\n", name, ct, len(content), preview)
}
