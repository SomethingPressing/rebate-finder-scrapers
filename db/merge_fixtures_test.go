package db

// merge_fixtures_test.go — golden fixtures for the promoter merge logic.
//
// The v0.8 broker (rebate-finder-broker) ports mergePromoterGroup,
// collectPromoterZips, deriveSegment, and promoterSourceRank to TypeScript,
// and the plan requires the port to produce IDENTICAL results. This test
// anchors both sides to one file:
//
//   - testdata/merge_fixtures.json holds inputs and the Go implementation's
//     outputs for a set of deliberately tricky cases.
//   - This test recomputes the outputs and fails if they drift from the file,
//     so a Go-side behaviour change cannot slip through silently.
//   - The broker repo copies the same file and asserts its TypeScript port
//     produces the same outputs (src/promoter/merge.test.ts).
//
// To regenerate after an intentional behaviour change:
//
//	UPDATE_MERGE_FIXTURES=1 go test ./db -run TestMergeFixtures
//
// then copy the file into rebate-finder-broker/src/promoter/__fixtures__/.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/incenva/rebate-scraper/models"
)

const fixturesPath = "../testdata/merge_fixtures.json"

// fixtureRow is the JSON shape of one staging row input. Field names mirror
// the staging table's column names so the TypeScript side reads naturally.
type fixtureRow struct {
	Source               string   `json:"source"`
	ProgramName          string   `json:"program_name"`
	UtilityCompany       string   `json:"utility_company"`
	IncentiveDescription *string  `json:"incentive_description"`
	IncentiveAmount      *float64 `json:"incentive_amount"`
	MaximumAmount        *float64 `json:"maximum_amount"`
	PercentValue         *float64 `json:"percent_value"`
	PerUnitAmount        *float64 `json:"per_unit_amount"`
	IncentiveFormat      *string  `json:"incentive_format"`
	UnitType             *string  `json:"unit_type"`
	State                *string  `json:"state"`
	ZipCode              *string  `json:"zip_code"`
	ZipCodes             []string `json:"zip_codes"`
	ServiceTerritory     *string  `json:"service_territory"`
	AvailableNationwide  *bool    `json:"available_nationwide"`
	CategoryTag          []string `json:"category_tag"`
	Segment              []string `json:"segment"`
	CustomerType         *string  `json:"customer_type"`
	Administrator        *string  `json:"administrator"`
	ImplementingSector   *string  `json:"implementing_sector"`
	StartDate            *string  `json:"start_date"`
	EndDate              *string  `json:"end_date"`
	WhileFundsLast       *bool    `json:"while_funds_last"`
	ApplicationURL       *string  `json:"application_url"`
	ApplicationProcess   *string  `json:"application_process"`
	ProgramURL           *string  `json:"program_url"`
	SourceURL            *string  `json:"source_url"`
	ContactEmail         *string  `json:"contact_email"`
	ContactPhone         *string  `json:"contact_phone"`
	ImageURL             *string  `json:"image_url"`
	ImageURLs            []string `json:"image_urls"`
	ContractorRequired   *bool    `json:"contractor_required"`
	EnergyAuditRequired  *bool    `json:"energy_audit_required"`
	RateTiers            []models.RateTier `json:"rate_tiers"`
}

type fixtureCase struct {
	Name     string                 `json:"name"`
	Priority []string               `json:"priority"`
	Rows     []fixtureRow           `json:"rows"`
	Expected map[string]interface{} `json:"expected"`
}

type fixtureFile struct {
	Description string        `json:"description"`
	Cases       []fixtureCase `json:"cases"`
}

func (fr fixtureRow) toStagedRebate() models.StagedRebate {
	return models.StagedRebate{
		Source:               fr.Source,
		ProgramName:          fr.ProgramName,
		UtilityCompany:       fr.UtilityCompany,
		IncentiveDescription: fr.IncentiveDescription,
		IncentiveAmount:      fr.IncentiveAmount,
		MaximumAmount:        fr.MaximumAmount,
		PercentValue:         fr.PercentValue,
		PerUnitAmount:        fr.PerUnitAmount,
		IncentiveFormat:      fr.IncentiveFormat,
		UnitType:             fr.UnitType,
		State:                fr.State,
		ZipCode:              fr.ZipCode,
		ZipCodes:             models.StringSlice(fr.ZipCodes),
		ServiceTerritory:     fr.ServiceTerritory,
		AvailableNationwide:  fr.AvailableNationwide,
		CategoryTag:          models.StringSlice(fr.CategoryTag),
		Segment:              models.StringSlice(fr.Segment),
		CustomerType:         fr.CustomerType,
		Administrator:        fr.Administrator,
		ImplementingSector:   fr.ImplementingSector,
		StartDate:            fr.StartDate,
		EndDate:              fr.EndDate,
		WhileFundsLast:       fr.WhileFundsLast,
		ApplicationURL:       fr.ApplicationURL,
		ApplicationProcess:   fr.ApplicationProcess,
		ProgramURL:           fr.ProgramURL,
		SourceURL:            fr.SourceURL,
		ContactEmail:         fr.ContactEmail,
		ContactPhone:         fr.ContactPhone,
		ImageURL:             fr.ImageURL,
		ImageURLs:            models.StringSlice(fr.ImageURLs),
		ContractorRequired:   fr.ContractorRequired,
		EnergyAuditRequired:  fr.EnergyAuditRequired,
		RateTiers:            models.RateTiersJSON(fr.RateTiers),
	}
}

// computeExpected runs the real promoter pipeline pieces on a case:
// priority sort → mergePromoterGroup → deriveSegment → collectPromoterZips.
func computeExpected(c fixtureCase) map[string]interface{} {
	rows := make([]models.StagedRebate, len(c.Rows))
	for i, fr := range c.Rows {
		rows[i] = fr.toStagedRebate()
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return promoterSourceRank(rows[i].Source, c.Priority) <
			promoterSourceRank(rows[j].Source, c.Priority)
	})

	m := mergePromoterGroup(rows)
	zips := collectPromoterZips(rows)
	segment := deriveSegment(m)

	strOrNil := func(v *string) interface{} {
		if v == nil {
			return nil
		}
		return *v
	}
	numOrNil := func(v *float64) interface{} {
		if v == nil {
			return nil
		}
		return *v
	}
	boolOrNil := func(v *bool) interface{} {
		if v == nil {
			return nil
		}
		return *v
	}
	arr := func(v []string) interface{} {
		if v == nil {
			return []string{}
		}
		return v
	}

	tiers := []models.RateTier(m.rateTiers)
	if tiers == nil {
		tiers = []models.RateTier{}
	}

	return map[string]interface{}{
		"program_name":          m.programName,
		"utility_company":       m.utilityCompany,
		"incentive_description": strOrNil(m.incentiveDescription),
		"incentive_amount":      numOrNil(m.incentiveAmount),
		"maximum_amount":        numOrNil(m.maximumAmount),
		"percent_value":         numOrNil(m.percentValue),
		"per_unit_amount":       numOrNil(m.perUnitAmount),
		"incentive_format":      strOrNil(m.incentiveFormat),
		"unit_type":             strOrNil(m.unitType),
		"state":                 strOrNil(m.state),
		"service_territory":     strOrNil(m.serviceTerritory),
		"available_nationwide":  boolOrNil(m.availableNationwide),
		"customer_type":         strOrNil(m.customerType),
		"administrator":         strOrNil(m.administrator),
		"implementing_sector":   strOrNil(m.implementingSector),
		"start_date":            strOrNil(m.startDate),
		"end_date":              strOrNil(m.endDate),
		"while_funds_last":      boolOrNil(m.whileFundsLast),
		"application_url":       strOrNil(m.applicationURL),
		"application_process":   strOrNil(m.applicationProcess),
		"program_url":           strOrNil(m.programURL),
		"source_url":            strOrNil(m.sourceURL),
		"contact_email":         strOrNil(m.contactEmail),
		"contact_phone":         strOrNil(m.contactPhone),
		"image_url":             strOrNil(m.imageURL),
		"contractor_required":   boolOrNil(m.contractorRequired),
		"energy_audit_required": boolOrNil(m.energyAuditRequired),
		"category_tag":          arr(m.categoryTag),
		"segment":               arr(segment),
		"image_urls":            arr(m.imageURLs),
		"sources":               arr(m.sources),
		"rate_tiers":            tiers,
		"zips":                  arr(zips),
	}
}

// fixtureKeyByField maps every field of promoterMerged to the key it is
// serialized under. TestFixtureCoversEveryMergedField asserts this map stays
// exhaustive, which is the guard that matters: without it, a field added to
// promoterMerged is simply absent from the fixtures, and the TypeScript port
// in rebate-finder-broker can silently fail to implement it while every
// parity test still passes. That is exactly how contractor_required and
// energy_audit_required went missing.
var fixtureKeyByField = map[string]string{
	"programName":          "program_name",
	"utilityCompany":       "utility_company",
	"incentiveDescription": "incentive_description",
	"incentiveAmount":      "incentive_amount",
	"maximumAmount":        "maximum_amount",
	"percentValue":         "percent_value",
	"perUnitAmount":        "per_unit_amount",
	"incentiveFormat":      "incentive_format",
	"unitType":             "unit_type",
	"state":                "state",
	"serviceTerritory":     "service_territory",
	"availableNationwide":  "available_nationwide",
	"categoryTag":          "category_tag",
	"segment":              "segment",
	"customerType":         "customer_type",
	"administrator":        "administrator",
	"implementingSector":   "implementing_sector",
	"sources":              "sources",
	"startDate":            "start_date",
	"endDate":              "end_date",
	"whileFundsLast":       "while_funds_last",
	"applicationURL":       "application_url",
	"applicationProcess":   "application_process",
	"programURL":           "program_url",
	"sourceURL":            "source_url",
	"contactEmail":         "contact_email",
	"contactPhone":         "contact_phone",
	"imageURL":             "image_url",
	"imageURLs":            "image_urls",
	"contractorRequired":   "contractor_required",
	"energyAuditRequired":  "energy_audit_required",
	"rateTiers":            "rate_tiers",
}

// TestFixtureCoversEveryMergedField fails when promoterMerged gains a field
// that the fixtures do not carry. Add the field to fixtureKeyByField AND to
// the map returned by computeExpected, then regenerate the fixtures and
// implement it in the broker's TypeScript port.
func TestFixtureCoversEveryMergedField(t *testing.T) {
	typ := reflect.TypeOf(promoterMerged{})
	serialized := computeExpected(fixtureCases()[0])

	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i).Name
		key, mapped := fixtureKeyByField[field]
		if !mapped {
			t.Errorf("promoterMerged.%s is not in fixtureKeyByField — the fixtures (and therefore the "+
				"broker's TypeScript merge) would silently ignore it", field)
			continue
		}
		if _, present := serialized[key]; !present {
			t.Errorf("promoterMerged.%s maps to fixture key %q, but computeExpected does not emit it", field, key)
		}
	}
}

func s(v string) *string    { return &v }
func f(v float64) *float64  { return &v }
func b(v bool) *bool        { return &v }

// fixtureCases defines the inputs. Expected outputs are always computed from
// the live Go implementation, never hand-written.
func fixtureCases() []fixtureCase {
	return []fixtureCase{
		{
			Name:     "single row, all scalars set",
			Priority: DefaultSourcePriority,
			Rows: []fixtureRow{{
				Source: "dsireusa", ProgramName: "Heat Pump Rebate", UtilityCompany: "Consumers Energy",
				IncentiveDescription: s("Up to $500 back"), IncentiveAmount: f(500), MaximumAmount: f(1000),
				IncentiveFormat: s("dollar_amount"), UnitType: s("unit"), State: s("MI"),
				ZipCodes: []string{"48201", "48202"}, ServiceTerritory: s("Lower Peninsula"),
				AvailableNationwide: b(false), CategoryTag: []string{"HVAC"}, Segment: []string{"Residential"},
				CustomerType: s("Residential"), StartDate: s("2026-01-01"), WhileFundsLast: b(true),
				ProgramURL: s("https://example.com/hp"), ContactEmail: s("info@example.com"),
			}},
		},
		{
			Name:     "two sources: priority wins scalars, secondary fills gaps",
			Priority: DefaultSourcePriority,
			Rows: []fixtureRow{
				{
					Source: "dsireusa", ProgramName: "DSIRE Name", UtilityCompany: "Xcel Energy",
					IncentiveAmount: f(300), State: s("CO"),
				},
				{
					Source: "rewiring_america", ProgramName: "RA Name", UtilityCompany: "Xcel Energy",
					IncentiveAmount: f(999), IncentiveDescription: s("RA description"),
					MaximumAmount: f(2500), State: s("CO"), ProgramURL: s("https://ra.example.com"),
				},
			},
		},
		{
			Name:     "empty-string text is skipped, not picked",
			Priority: DefaultSourcePriority,
			Rows: []fixtureRow{
				{Source: "rewiring_america", ProgramName: "P", UtilityCompany: "U", IncentiveDescription: s("   "), ContactPhone: s("")},
				{Source: "dsireusa", ProgramName: "P2", UtilityCompany: "U", IncentiveDescription: s("real description"), ContactPhone: s("555-0100")},
			},
		},
		{
			Name:     "unlisted source ranks last, listed order preserved",
			Priority: []string{"dsireusa"},
			Rows: []fixtureRow{
				{Source: "consumers_energy_pdf", ProgramName: "PDF Name", UtilityCompany: "CE", IncentiveAmount: f(750)},
				{Source: "dsireusa", ProgramName: "DSIRE Name", UtilityCompany: "CE", MaximumAmount: f(100)},
				{Source: "energy_star", ProgramName: "ES Name", UtilityCompany: "CE", PercentValue: f(30)},
			},
		},
		{
			Name:     "array union dedups case-insensitively, first casing wins",
			Priority: DefaultSourcePriority,
			Rows: []fixtureRow{
				{Source: "rewiring_america", ProgramName: "P", UtilityCompany: "U", CategoryTag: []string{"HVAC", "Solar"}, ImageURLs: []string{"https://a/1.png"}},
				{Source: "dsireusa", ProgramName: "P", UtilityCompany: "U", CategoryTag: []string{"hvac", "Water Heating"}, ImageURLs: []string{"https://a/1.png", "https://a/2.png"}},
			},
		},
		{
			Name:     "segment normalization: variants map, junk drops, multi-segment expands",
			Priority: DefaultSourcePriority,
			Rows: []fixtureRow{
				{Source: "dsireusa", ProgramName: "P", UtilityCompany: "U",
					Segment: []string{"residential & commercial", "Schools", "Utility", "all sectors", "MULTIFAMILY"}},
			},
		},
		{
			Name:     "segment fallback derives from customer_type when segment empty",
			Priority: DefaultSourcePriority,
			Rows: []fixtureRow{
				{Source: "energy_star", ProgramName: "P", UtilityCompany: "U", CustomerType: s("Residential, Commercial")},
			},
		},
		{
			Name:     "rate tiers: primary empty, first non-empty later row wins",
			Priority: DefaultSourcePriority,
			Rows: []fixtureRow{
				{Source: "rewiring_america", ProgramName: "P", UtilityCompany: "U"},
				{Source: "dsireusa", ProgramName: "P", UtilityCompany: "U",
					RateTiers: []models.RateTier{{ID: "t1", Description: "First 100 kWh", Amount: 0.5, Unit: "kwh"}}},
			},
		},
		{
			Name:     "zips: zip_code and zip_codes union, dedup, sorted",
			Priority: DefaultSourcePriority,
			Rows: []fixtureRow{
				{Source: "rewiring_america", ProgramName: "P", UtilityCompany: "U", ZipCode: s("48202"), ZipCodes: []string{"48201", "48225"}},
				{Source: "dsireusa", ProgramName: "P", UtilityCompany: "U", ZipCodes: []string{"48202", "48101"}},
			},
		},
		{
			Name:     "bool pick takes first non-nil even when false",
			Priority: DefaultSourcePriority,
			Rows: []fixtureRow{
				{Source: "rewiring_america", ProgramName: "P", UtilityCompany: "U"},
				{Source: "dsireusa", ProgramName: "P", UtilityCompany: "U", WhileFundsLast: b(false), AvailableNationwide: b(true)},
			},
		},
		{
			// contractor_required / energy_audit_required were merged by Go but
			// missing from these fixtures, so the broker's port dropped them
			// unnoticed. This case pins both, including false-beats-nil.
			Name:     "requirement flags merge like any other bool",
			Priority: DefaultSourcePriority,
			Rows: []fixtureRow{
				{Source: "rewiring_america", ProgramName: "P", UtilityCompany: "U", ContractorRequired: b(false)},
				{Source: "dsireusa", ProgramName: "P", UtilityCompany: "U", ContractorRequired: b(true), EnergyAuditRequired: b(true)},
			},
		},
		{
			Name:     "duplicate source rows: sources list dedups, order kept",
			Priority: DefaultSourcePriority,
			Rows: []fixtureRow{
				{Source: "dsireusa", ProgramName: "P", UtilityCompany: "U", IncentiveAmount: f(1)},
				{Source: "dsireusa", ProgramName: "P alt", UtilityCompany: "U", IncentiveAmount: f(2)},
				{Source: "energy_star", ProgramName: "P es", UtilityCompany: "U"},
			},
		},
	}
}

// TestMergeFixtures verifies testdata/merge_fixtures.json matches the current
// Go implementation, or regenerates it when UPDATE_MERGE_FIXTURES=1.
func TestMergeFixtures(t *testing.T) {
	cases := fixtureCases()
	for i := range cases {
		cases[i].Expected = computeExpected(cases[i])
	}
	file := fixtureFile{
		Description: "Golden fixtures for the promoter merge port. Inputs defined in db/merge_fixtures_test.go; expected outputs computed by the Go implementation. The broker repo's src/promoter/merge.test.ts must produce identical results. Regenerate: UPDATE_MERGE_FIXTURES=1 go test ./db -run TestMergeFixtures",
		Cases:       cases,
	}

	if os.Getenv("UPDATE_MERGE_FIXTURES") == "1" {
		data, err := json.MarshalIndent(file, "", "  ")
		if err != nil {
			t.Fatalf("marshal fixtures: %v", err)
		}
		if err := os.MkdirAll(filepath.Dir(fixturesPath), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(fixturesPath, append(data, '\n'), 0o644); err != nil {
			t.Fatalf("write fixtures: %v", err)
		}
		t.Logf("wrote %s (%d cases)", fixturesPath, len(cases))
		return
	}

	raw, err := os.ReadFile(fixturesPath)
	if err != nil {
		t.Fatalf("read %s (run with UPDATE_MERGE_FIXTURES=1 to generate): %v", fixturesPath, err)
	}
	var onDisk fixtureFile
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("parse fixtures: %v", err)
	}

	// Round-trip the computed file through JSON so numeric types compare like
	// with like (interface{} floats on both sides).
	computedJSON, _ := json.Marshal(file)
	var computed fixtureFile
	_ = json.Unmarshal(computedJSON, &computed)

	if len(onDisk.Cases) != len(computed.Cases) {
		t.Fatalf("fixture drift: %d cases on disk, %d computed — regenerate with UPDATE_MERGE_FIXTURES=1 and update the broker copy", len(onDisk.Cases), len(computed.Cases))
	}
	for i := range computed.Cases {
		if !reflect.DeepEqual(onDisk.Cases[i].Expected, computed.Cases[i].Expected) {
			t.Errorf("fixture drift in case %q — the Go merge behaviour changed. Regenerate with UPDATE_MERGE_FIXTURES=1 and update the broker copy", computed.Cases[i].Name)
		}
	}
}
