package models

import "testing"

func sampleRow() StagedRebate {
	amount := 500.0
	state := "MI"
	url := "https://example.com/program"
	return StagedRebate{
		ProgramName:     "Heat Pump Rebate",
		UtilityCompany:  "Consumers Energy",
		IncentiveAmount: &amount,
		State:           &state,
		ProgramURL:      &url,
		CategoryTag:     StringSlice{"HVAC", "Electrification"},
		ZipCodes:        StringSlice{"48201", "48202"},
		Source:          "consumers_energy",
	}
}

func TestComputeContentHashDeterministic(t *testing.T) {
	a, b := sampleRow(), sampleRow()
	if ComputeContentHash(&a) != ComputeContentHash(&b) {
		t.Fatal("identical rows must produce identical content hashes")
	}
}

func TestComputeContentHashIgnoresArrayOrder(t *testing.T) {
	a, b := sampleRow(), sampleRow()
	b.CategoryTag = StringSlice{"Electrification", "HVAC"}
	b.ZipCodes = StringSlice{"48202", "48201"}
	if ComputeContentHash(&a) != ComputeContentHash(&b) {
		t.Fatal("array order must not affect the content hash")
	}
}

func TestComputeContentHashDetectsDataChange(t *testing.T) {
	a, b := sampleRow(), sampleRow()
	newAmount := 750.0
	b.IncentiveAmount = &newAmount
	if ComputeContentHash(&a) == ComputeContentHash(&b) {
		t.Fatal("a data change must change the content hash")
	}
}

func TestComputeContentHashIgnoresLifecycleAndProvenance(t *testing.T) {
	a, b := sampleRow(), sampleRow()
	b.Source = "dsireusa"
	b.ScraperVersion = "9.9"
	b.PromotionStatus = PromotionPromoted
	b.ReleaseStatus = ReleasePending
	raw := "<html>changed</html>"
	b.StgRawResponse = &raw
	if ComputeContentHash(&a) != ComputeContentHash(&b) {
		t.Fatal("lifecycle and provenance fields must not affect the content hash")
	}
}

func TestComputeContentHashNilVsEmpty(t *testing.T) {
	a, b := sampleRow(), sampleRow()
	empty := ""
	b.IncentiveDescription = &empty
	// nil and empty-string serialize identically — this is deliberate: sources
	// flip between the two without any user-visible change.
	if ComputeContentHash(&a) != ComputeContentHash(&b) {
		t.Fatal("nil and empty string should hash identically")
	}
}
