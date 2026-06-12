package evaluator

import (
	"log"

	"github.com/incenva/rebate-scraper/models"
)

// fieldPopEvaluator handles JS-rendered sources (e.g. Salesforce Experience Cloud,
// React SPAs) where a plain HTTP re-fetch returns only an app shell — making LLM
// extraction meaningless.
//
// Instead of fetching or calling GPT-4o, it scores the staged DB row directly:
// each field is StatusPopulated (scraper captured a value) or StatusUnpopulated
// (scraper left the field blank).  The score therefore measures scraper coverage,
// not content accuracy.
//
// Register new JS-rendered sources in strategy.go's registry to use this evaluator.
type fieldPopEvaluator struct{}

func (e *fieldPopEvaluator) Mode() string   { return "field_population" }
func (e *fieldPopEvaluator) UsesLLM() bool { return false }

func (e *fieldPopEvaluator) EvaluateDB(cfg Config, row models.StagedRebate) EvalResult {
	res := buildBaseResult(row)
	res.EvalMode = e.Mode()
	url := res.ProgramURL
	if url == "" {
		url = res.SourceURL
	}
	log.Printf("  → field population (JS-rendered): %s", url)
	scores := ScoreFieldPopulation(row)
	res.FieldScores = scores
	res.OverallScore = OverallScore(scores)
	res.MissingFields = MissingFields(scores)
	return res
}

func (e *fieldPopEvaluator) EvaluateTestcase(cfg Config, tc TestCase, staged *models.StagedRebate) EvalResult {
	res := buildBaseTestcaseResult(tc)
	res.EvalMode = e.Mode()
	if staged == nil {
		res.Error = "no staged row — scraper has not stored this URL yet"
		return res
	}
	res.ProgramName = staged.ProgramName
	res.DBValues = stagedToDBValues(*staged)
	scores := ScoreFieldPopulation(*staged)
	res.FieldScores = scores
	res.OverallScore = OverallScore(scores)
	res.MissingFields = MissingFields(scores)
	return res
}
