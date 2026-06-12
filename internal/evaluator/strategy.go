package evaluator

import "github.com/incenva/rebate-scraper/models"

// SourceEvaluator defines the evaluation strategy for a scraper source.
// Each source type (plain HTML, cached API responses, JS-rendered SPAs) implements
// its own fetch and scoring behaviour.  Register new source types in the registry
// below; sources not listed fall back to htmlEvaluator (live HTTP + LLM diff).
type SourceEvaluator interface {
	// Mode returns the eval_mode label written into EvalResult.
	// Empty string means the default LLM-comparison mode.
	Mode() string

	// UsesLLM reports whether this evaluator makes OpenAI API calls.
	// The runner uses this to throttle requests and stay within rate limits.
	UsesLLM() bool

	// EvaluateDB scores one staged DB row (used in 'db' evaluation mode).
	EvaluateDB(cfg Config, row models.StagedRebate) EvalResult

	// EvaluateTestcase scores a curated test-case URL against its staged row
	// (used in 'testcases' evaluation mode).
	// staged is nil when the scraper has not yet stored the URL.
	EvaluateTestcase(cfg Config, tc TestCase, staged *models.StagedRebate) EvalResult
}

// registry maps scraper source names to their SourceEvaluator.
// To add a new source type, register it here — no changes needed elsewhere.
var registry = map[string]SourceEvaluator{
	// API-based sources: raw JSON is cached in stg_raw_response; no live re-fetch needed.
	"dsireusa":    &cachedEvaluator{},
	"energy_star": &cachedEvaluator{},

	// JS-rendered sources: plain HTTP returns only an app shell; score staged DB data directly.
	"xcel_energy": &fieldPopEvaluator{},
}

// evaluatorFor returns the SourceEvaluator registered for the given scraper source.
// Falls back to htmlEvaluator for any source not in the registry.
func evaluatorFor(source string) SourceEvaluator {
	if ev, ok := registry[source]; ok {
		return ev
	}
	return &htmlEvaluator{}
}

// buildBaseResult constructs the common EvalResult fields from a staged DB row.
func buildBaseResult(row models.StagedRebate) EvalResult {
	programURL := ""
	if row.ProgramURL != nil && *row.ProgramURL != "" {
		programURL = *row.ProgramURL
	} else if row.ApplicationURL != nil && *row.ApplicationURL != "" {
		programURL = *row.ApplicationURL
	}
	return EvalResult{
		Source:      row.Source,
		ProgramName: row.ProgramName,
		SourceID:    row.SourceID,
		ProgramURL:  programURL,
		SourceURL:   resolveSourceURL(row),
		DBValues:    stagedToDBValues(row),
	}
}

// buildBaseTestcaseResult constructs the common EvalResult fields from a test case descriptor.
func buildBaseTestcaseResult(tc TestCase) EvalResult {
	return EvalResult{
		Source:      tc.Source,
		ProgramName: tc.Description,
		SourceID:    tc.ID,
		ContentType: tc.ContentType,
	}
}
