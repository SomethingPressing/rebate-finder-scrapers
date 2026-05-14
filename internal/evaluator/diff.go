package evaluator

import (
	"fmt"
	"math"
	"strings"

	"github.com/incenva/rebate-scraper/internal/llm"
	"github.com/incenva/rebate-scraper/models"
)

// FieldStatus is the comparison result for one field.
type FieldStatus string

const (
	StatusMatch       FieldStatus = "✓ match"
	StatusPartial     FieldStatus = "~ partial"
	StatusMissing     FieldStatus = "✗ missing"    // scraper empty, LLM found it
	StatusMismatch    FieldStatus = "✗ mismatch"   // both have values but they disagree
	StatusEmptyBoth   FieldStatus = "  empty"      // neither side found it
	StatusScraperOnly FieldStatus = "? scraper-only" // scraper has it, LLM does not
)

// FieldScore holds the per-field comparison result.
type FieldScore struct {
	Name         string
	Status       FieldStatus
	ScraperValue string
	LLMValue     string
	Weight       float64
}

// DiffFieldsFresh compares a freshly re-extracted Incentive against an LLM
// extraction.  Use this instead of DiffFields when the scraper side has been
// re-computed from live content so scores always reflect current code.
func DiffFieldsFresh(inc *models.Incentive, ext *llm.LLMExtraction) []FieldScore {
	return []FieldScore{
		cmpStr("program_name", inc.ProgramName, ext.ProgramName, 2.0),
		cmpStr("utility_company", inc.UtilityCompany, ext.UtilityCompany, 1.5),
		cmpStrPtr("incentive_description", inc.IncentiveDescription, ext.IncentiveDescription, 1.0),
		cmpFloat("incentive_amount", inc.IncentiveAmount, ext.IncentiveAmount, 2.0),
		cmpFloat("maximum_amount", inc.MaximumAmount, ext.MaximumAmount, 1.5),
		cmpFloat("percent_value", inc.PercentValue, ext.PercentValue, 1.5),
		cmpFloat("per_unit_amount", inc.PerUnitAmount, ext.PerUnitAmount, 1.5),
		cmpStrPtr("incentive_format", inc.IncentiveFormat, ext.IncentiveFormat, 1.0),
		cmpStrPtr("state", inc.State, ext.State, 1.0),
		cmpSlice("categories", inc.CategoryTag, ext.Categories, 1.5),
		cmpStrPtr("customer_type", inc.CustomerType, ext.CustomerType, 1.0),
		cmpStrPtr("start_date", inc.StartDate, ext.StartDate, 1.0),
		cmpStrPtr("end_date", inc.EndDate, ext.EndDate, 1.0),
		cmpStrPtr("application_url", inc.ApplicationURL, ext.ApplicationURL, 1.0),
		cmpStrPtr("contact_email", inc.ContactEmail, ext.ContactEmail, 0.5),
		cmpStrPtr("contact_phone", inc.ContactPhone, ext.ContactPhone, 0.5),
		cmpBool("contractor_required", inc.ContractorRequired, ext.ContractorRequired, 0.5),
		cmpBool("energy_audit_required", inc.EnergyAuditRequired, ext.EnergyAuditRequired, 0.5),
	}
}

// DiffFields compares a staged row against an LLM extraction and returns per-field scores.
func DiffFields(staged models.StagedRebate, ext *llm.LLMExtraction) []FieldScore {
	return []FieldScore{
		cmpStr("program_name", staged.ProgramName, ext.ProgramName, 2.0),
		cmpStr("utility_company", staged.UtilityCompany, ext.UtilityCompany, 1.5),
		cmpStrPtr("incentive_description", staged.IncentiveDescription, ext.IncentiveDescription, 1.0),
		cmpFloat("incentive_amount", staged.IncentiveAmount, ext.IncentiveAmount, 2.0),
		cmpFloat("maximum_amount", staged.MaximumAmount, ext.MaximumAmount, 1.5),
		cmpFloat("percent_value", staged.PercentValue, ext.PercentValue, 1.5),
		cmpFloat("per_unit_amount", staged.PerUnitAmount, ext.PerUnitAmount, 1.5),
		cmpStrPtr("incentive_format", staged.IncentiveFormat, ext.IncentiveFormat, 1.0),
		cmpStrPtr("state", staged.State, ext.State, 1.0),
		cmpSlice("categories", []string(staged.CategoryTag), ext.Categories, 1.5),
		cmpStrPtr("customer_type", staged.CustomerType, ext.CustomerType, 1.0),
		cmpStrPtr("start_date", staged.StartDate, ext.StartDate, 1.0),
		cmpStrPtr("end_date", staged.EndDate, ext.EndDate, 1.0),
		cmpStrPtr("application_url", staged.ApplicationURL, ext.ApplicationURL, 1.0),
		cmpStrPtr("contact_email", staged.ContactEmail, ext.ContactEmail, 0.5),
		cmpStrPtr("contact_phone", staged.ContactPhone, ext.ContactPhone, 0.5),
		cmpBool("contractor_required", staged.ContractorRequired, ext.ContractorRequired, 0.5),
		cmpBool("energy_audit_required", staged.EnergyAuditRequired, ext.EnergyAuditRequired, 0.5),
	}
}

// OverallScore returns a 0–1 score: matched weight / total countable weight.
// Fields where neither side found a value (empty_both) are excluded from the denominator.
func OverallScore(scores []FieldScore) float64 {
	var total, matched float64
	for _, s := range scores {
		if s.Status == StatusEmptyBoth {
			continue
		}
		total += s.Weight
		switch s.Status {
		case StatusMatch:
			matched += s.Weight
		case StatusPartial:
			matched += s.Weight * 0.5
		}
	}
	if total == 0 {
		return 0
	}
	return matched / total
}

// MissingFields returns field names where the scraper is empty but the LLM found a value.
func MissingFields(scores []FieldScore) []string {
	var out []string
	for _, s := range scores {
		if s.Status == StatusMissing || s.Status == StatusMismatch {
			out = append(out, s.Name)
		}
	}
	return out
}

// ── comparison helpers ────────────────────────────────────────────────────────

func cmpStr(name, scraper, lm string, w float64) FieldScore {
	s := strings.TrimSpace(scraper)
	l := strings.TrimSpace(lm)
	return FieldScore{
		Name:         name,
		Status:       scoreStrings(s, l),
		ScraperValue: trunc(s, 38),
		LLMValue:     trunc(l, 38),
		Weight:       w,
	}
}

// cmpStrPtr compares a *string from the staged row against a plain string from the LLM.
func cmpStrPtr(name string, scraper *string, lm string, w float64) FieldScore {
	s := derefStr(scraper)
	return cmpStr(name, s, lm, w)
}

func cmpFloat(name string, scraper *float64, lm *float64, w float64) FieldScore {
	sv, lv := "", ""
	if scraper != nil && *scraper != 0 {
		sv = fmt.Sprintf("%.2f", *scraper)
	}
	if lm != nil && *lm != 0 {
		lv = fmt.Sprintf("%.2f", *lm)
	}

	var status FieldStatus
	switch {
	case sv == "" && lv == "":
		status = StatusEmptyBoth
	case sv == "" && lv != "":
		status = StatusMissing
	case sv != "" && lv == "":
		status = StatusScraperOnly
	default:
		diff := math.Abs(*scraper-*lm) / math.Max(math.Abs(*lm), 0.01)
		if diff < 0.05 {
			status = StatusMatch
		} else {
			status = StatusMismatch
		}
	}
	return FieldScore{Name: name, Status: status, ScraperValue: sv, LLMValue: lv, Weight: w}
}

func cmpBool(name string, scraper *bool, lm *bool, w float64) FieldScore {
	sv, lv := "", ""
	if scraper != nil {
		sv = fmt.Sprintf("%v", *scraper)
	}
	if lm != nil {
		lv = fmt.Sprintf("%v", *lm)
	}

	var status FieldStatus
	switch {
	case sv == "" && lv == "":
		status = StatusEmptyBoth
	case sv == "" && lv != "":
		status = StatusMissing
	case sv != "" && lv == "":
		status = StatusScraperOnly
	case sv == lv:
		status = StatusMatch
	default:
		status = StatusMismatch
	}
	return FieldScore{Name: name, Status: status, ScraperValue: sv, LLMValue: lv, Weight: w}
}

func cmpSlice(name string, scraper, lm []string, w float64) FieldScore {
	scraperSet := lowerSet(scraper)
	lmSet := lowerSet(lm)

	if len(scraperSet) == 0 && len(lmSet) == 0 {
		return FieldScore{Name: name, Status: StatusEmptyBoth, Weight: w}
	}
	if len(scraperSet) == 0 {
		return FieldScore{Name: name, Status: StatusMissing, LLMValue: strings.Join(lm, ", "), Weight: w}
	}
	if len(lmSet) == 0 {
		return FieldScore{Name: name, Status: StatusScraperOnly, ScraperValue: strings.Join(scraper, ", "), Weight: w}
	}

	var inter, union int
	for k := range lmSet {
		union++
		if scraperSet[k] {
			inter++
		}
	}
	for k := range scraperSet {
		if !lmSet[k] {
			union++
		}
	}

	j := float64(inter) / float64(union)
	var status FieldStatus
	switch {
	case j >= 0.8:
		status = StatusMatch
	case j >= 0.3:
		status = StatusPartial
	default:
		status = StatusMissing
	}
	return FieldScore{
		Name:         name,
		Status:       status,
		ScraperValue: strings.Join(scraper, ", "),
		LLMValue:     strings.Join(lm, ", "),
		Weight:       w,
	}
}

func scoreStrings(s, l string) FieldStatus {
	sl, ll := strings.ToLower(s), strings.ToLower(l)
	switch {
	case s == "" && l == "":
		return StatusEmptyBoth
	case s == "" && l != "":
		return StatusMissing
	case s != "" && l == "":
		return StatusScraperOnly
	case sl == ll:
		return StatusMatch
	// treat scraper description truncation as a partial match
	case len(s) >= 490 && strings.Contains(ll, sl[:min(len(sl), 100)]):
		return StatusPartial
	case strings.Contains(sl, ll) || strings.Contains(ll, sl):
		return StatusPartial
	default:
		return StatusMismatch
	}
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func lowerSet(in []string) map[string]bool {
	m := make(map[string]bool, len(in))
	for _, v := range in {
		m[strings.ToLower(strings.TrimSpace(v))] = true
	}
	return m
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
