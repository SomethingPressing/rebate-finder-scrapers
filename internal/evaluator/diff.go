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
	StatusMissing     FieldStatus = "✗ missing"      // scraper empty, LLM found it
	StatusMismatch    FieldStatus = "✗ mismatch"     // both have values but they disagree
	StatusEmptyBoth   FieldStatus = "  empty"        // neither side found it
	StatusScraperOnly FieldStatus = "? scraper-only" // scraper has it, LLM does not

	// Field-population mode (JS-rendered sources where LLM comparison is not feasible).
	StatusPopulated   FieldStatus = "✓ populated" // scraper captured the field
	StatusUnpopulated FieldStatus = "✗ empty"     // scraper left the field blank
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
// In field-population mode, StatusPopulated counts as a full match.
func OverallScore(scores []FieldScore) float64 {
	var total, matched float64
	for _, s := range scores {
		if s.Status == StatusEmptyBoth {
			continue
		}
		total += s.Weight
		switch s.Status {
		case StatusMatch, StatusPopulated:
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

// MissingFields returns field names where the scraper is empty or mismatched.
// Includes StatusUnpopulated for field-population mode.
func MissingFields(scores []FieldScore) []string {
	var out []string
	for _, s := range scores {
		if s.Status == StatusMissing || s.Status == StatusMismatch || s.Status == StatusUnpopulated {
			out = append(out, s.Name)
		}
	}
	return out
}

// ScoreFieldPopulation scores a staged DB row purely on which fields are non-empty.
// Used for JS-rendered sources (e.g. xcel_energy) where re-fetching would return
// a JavaScript shell and LLM comparison is not feasible.
// Each field is StatusPopulated (scraper captured it) or StatusUnpopulated (blank).
func ScoreFieldPopulation(row models.StagedRebate) []FieldScore {
	pop := func(name string, val *string, w float64) FieldScore {
		v := derefStr(val)
		if v != "" {
			return FieldScore{Name: name, Status: StatusPopulated, ScraperValue: trunc(v, 48), Weight: w}
		}
		return FieldScore{Name: name, Status: StatusUnpopulated, Weight: w}
	}
	popStr := func(name, val string, w float64) FieldScore {
		if val != "" {
			return FieldScore{Name: name, Status: StatusPopulated, ScraperValue: trunc(val, 48), Weight: w}
		}
		return FieldScore{Name: name, Status: StatusUnpopulated, Weight: w}
	}
	popFloat := func(name string, val *float64, w float64) FieldScore {
		if val != nil && *val != 0 {
			return FieldScore{Name: name, Status: StatusPopulated, ScraperValue: fmt.Sprintf("%.2f", *val), Weight: w}
		}
		return FieldScore{Name: name, Status: StatusUnpopulated, Weight: w}
	}
	popBool := func(name string, val *bool, w float64) FieldScore {
		if val != nil {
			return FieldScore{Name: name, Status: StatusPopulated, ScraperValue: fmt.Sprintf("%v", *val), Weight: w}
		}
		return FieldScore{Name: name, Status: StatusUnpopulated, Weight: w}
	}
	popSlice := func(name string, val []string, w float64) FieldScore {
		if len(val) > 0 {
			return FieldScore{Name: name, Status: StatusPopulated, ScraperValue: trunc(strings.Join(val, ", "), 48), Weight: w}
		}
		return FieldScore{Name: name, Status: StatusUnpopulated, Weight: w}
	}
	return []FieldScore{
		popStr("program_name", row.ProgramName, 2.0),
		popStr("utility_company", row.UtilityCompany, 1.5),
		pop("incentive_description", row.IncentiveDescription, 1.0),
		popFloat("incentive_amount", row.IncentiveAmount, 2.0),
		popFloat("maximum_amount", row.MaximumAmount, 1.5),
		popFloat("percent_value", row.PercentValue, 1.5),
		popFloat("per_unit_amount", row.PerUnitAmount, 1.5),
		pop("incentive_format", row.IncentiveFormat, 1.0),
		pop("state", row.State, 1.0),
		popSlice("categories", []string(row.CategoryTag), 1.5),
		pop("customer_type", row.CustomerType, 1.0),
		pop("start_date", row.StartDate, 1.0),
		pop("end_date", row.EndDate, 1.0),
		pop("application_url", row.ApplicationURL, 1.0),
		pop("contact_email", row.ContactEmail, 0.5),
		pop("contact_phone", row.ContactPhone, 0.5),
		popBool("contractor_required", row.ContractorRequired, 0.5),
		popBool("energy_audit_required", row.EnergyAuditRequired, 0.5),
	}
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
