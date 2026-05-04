// consumers_energy.go — 2026 Consumers Energy Business Energy Efficiency Programs.
//
// Extracts and logs incentive data for three measures from two PDFs:
//
//	Catalog     (128 pages): 2026 Incentive Catalog
//	Application  (53 pages): 2026 Incentive Application
//
// Page references
// ───────────────
//
//	Catalog PDF page = printed page number + 2  (cover + ToC eat two pages)
//	Application PDF page = printed page number  (no offset)
//
// Target measures
// ───────────────
//
//	#1  Air Conditioning — Split & Unitary (HV101)
//	    Catalog: PDF p.50 (printed p.48) — requirements & efficiency tables
//	    Application: PDF p.23 — incentive rate table ($30–$40 / ton)
//
//	#2  Interior Linear LED Tube Light Retrofits (LT101–LT126, LT207–LT209)
//	    Catalog: PDF p.13–14 (printed p.11–12) — requirements
//	    Application: PDF p.10–11 — incentive rates ($1–$10/lamp, $0.30–$1.00/watt)
//
//	#3  Refrigeration Compressors — Discus or Scroll (RL101, RL102)
//	    Catalog: PDF p.85 (printed p.83) — requirements & efficiency tables
//	    Application: PDF p.36 — incentive rates (Discus $20/ton, Scroll $40/ton)
//
// Data source: provided local PDF files.  Nothing is written to the database.
package scrapers

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/incenva/rebate-scraper/db"
	"github.com/incenva/rebate-scraper/models"
	"go.uber.org/zap"
)

// ── File paths ────────────────────────────────────────────────────────────────

// cePDFPaths holds the local paths to both source PDFs.
// Override at runtime via CONSUMERS_ENERGY_CATALOG_PDF and
// CONSUMERS_ENERGY_APPLICATION_PDF environment variables.
type cePDFPaths struct {
	Catalog     string
	Application string
}

func cePaths() cePDFPaths {
	return cePDFPaths{
		Catalog:     os.Getenv("CONSUMERS_ENERGY_CATALOG_PDF"),
		Application: os.Getenv("CONSUMERS_ENERGY_APPLICATION_PDF"),
	}
}

// ── Incentive specs ───────────────────────────────────────────────────────────

// ceIncentiveSpec describes one measure with its catalog and application pages.
type ceIncentiveSpec struct {
	Key            string      // machine-readable key
	MeasureName    string      // human title
	MeasureIDs     string      // IDs as printed in the docs (e.g. "HV101a–HV101j")
	Category       string      // section in catalog
	Description    string
	CatalogPages   []PageRange // PDF page numbers in the Catalog
	AppPages       []PageRange // PDF page numbers in the Application
	IncentiveRates []ceRate    // pre-extracted rates (dollar amounts)
}

// ceRate is a single incentive tier extracted from the Application PDF.
type ceRate struct {
	ID          string  // e.g. "HV101a"
	Description string  // e.g. "Split AC < 5.4 Tons, Min 14.3 SEER2"
	Amount      float64 // dollar amount
	Unit        string  // e.g. "$/Ton", "$/Lamp", "$/Watt Reduced"
}

// consumersEnergyIncentives — complete 2026 data for the three target measures.
var consumersEnergyIncentives = []ceIncentiveSpec{

	// ── #1  Air Conditioning — Split & Unitary AC Systems ─────────────────────
	{
		Key:         "hvac_air_conditioning",
		MeasureName: "Unitary (RTU) and Split (Including Heat Pumps) Air Conditioning Systems",
		MeasureIDs:  "HV101a–HV101j",
		Category:    "HVAC Equipment",
		Description: "Available for Consumers Energy electric customers installing new unitary single " +
			"package (RTU) or split (including heat pumps) air conditioning systems. " +
			"Incentive is based on the nameplate (nominal) cooling capacity (tons). " +
			"Pre-Notification required. Qualifies for New Construction and Retrofit.",
		CatalogPages: []PageRange{{Start: 50, End: 50}}, // printed p.48
		AppPages:     []PageRange{{Start: 23, End: 23}}, // printed p.23
		IncentiveRates: []ceRate{
			// Split Air Conditioning Systems (Including Heat Pumps)
			{ID: "HV101a", Description: "Split AC, < 5.4 Tons, Min 14.3 SEER2 (15 SEER)", Amount: 30.00, Unit: "$/Ton"},
			{ID: "HV101b", Description: "Split AC, ≥ 5.4 to < 11.25 Tons, Min 12.0 EER & 19.0 IEER", Amount: 40.00, Unit: "$/Ton"},
			{ID: "HV101c", Description: "Split AC, ≥ 11.25 to < 20 Tons, Min 12.0 EER & 16.8 IEER", Amount: 40.00, Unit: "$/Ton"},
			{ID: "HV101d", Description: "Split AC, ≥ 20 to 63 Tons, Min 12.5 EER & 15.5 IEER", Amount: 30.00, Unit: "$/Ton"},
			{ID: "HV101e", Description: "Split AC, > 63 Tons, Min 11.4 IEER", Amount: 30.00, Unit: "$/Ton"},
			// Unitary Air Conditioning Systems (RTU)
			{ID: "HV101f", Description: "Unitary RTU, < 5.4 Tons, Min 15.2 SEER2 (16.0 SEER)", Amount: 30.00, Unit: "$/Ton"},
			{ID: "HV101g", Description: "Unitary RTU, ≥ 5.4 to < 11.25 Tons, Min 12.0 EER & 19.0 IEER", Amount: 40.00, Unit: "$/Ton"},
			{ID: "HV101h", Description: "Unitary RTU, ≥ 11.25 to < 20 Tons, Min 12.0 EER & 16.8 IEER", Amount: 40.00, Unit: "$/Ton"},
			{ID: "HV101i", Description: "Unitary RTU, ≥ 20 to 63 Tons, Min 12.5 EER & 15.5 IEER", Amount: 30.00, Unit: "$/Ton"},
			{ID: "HV101j", Description: "Unitary RTU, > 63 Tons, Min 11.4 IEER", Amount: 30.00, Unit: "$/Ton"},
		},
	},

	// ── #2  Interior Linear LED Tube Light Retrofits ──────────────────────────
	{
		Key:         "interior_linear_led",
		MeasureName: "Interior Linear LED Tube Light Retrofits",
		MeasureIDs:  "LT101–LT126, LT207–LT209",
		Category:    "Lighting",
		Description: "Available for Consumers Energy electric customers replacing existing interior " +
			"T8, T12 or T5 fluorescent lamps with UL Type A, B, C or Dual Mode linear LED tube lights, " +
			"or replacing existing fixtures with new fixtures that use linear LED tube lights (LT207–LT209). " +
			"Incentive is per fluorescent lamp replaced or per watt reduced. Pre-Notification required.",
		CatalogPages: []PageRange{{Start: 13, End: 14}}, // printed p.11–12
		AppPages:     []PageRange{{Start: 10, End: 11}}, // printed p.10–11
		IncentiveRates: []ceRate{
			// UL Type A, B and Dual Mode (DM) — 2-ft lamps
			{ID: "LT101", Description: "2-ft T12 → 2-ft LED Tube (Type A/B/DM)", Amount: 2.50, Unit: "$/Lamp"},
			{ID: "LT102", Description: "2-ft T8 → 2-ft LED Tube (Type A/B/DM)", Amount: 1.00, Unit: "$/Lamp"},
			// 3-ft lamps
			{ID: "LT103", Description: "3-ft T12 → 3-ft LED Tube (Type A/B/DM)", Amount: 3.50, Unit: "$/Lamp"},
			{ID: "LT104", Description: "3-ft T8 → 3-ft LED Tube (Type A/B/DM)", Amount: 2.00, Unit: "$/Lamp"},
			// 4-ft lamps
			{ID: "LT105", Description: "4-ft T12 → One (1) 4-ft LED Tube (Type A/B/DM)", Amount: 5.00, Unit: "$/Lamp"},
			{ID: "LT106", Description: "4-ft T8 → One (1) 4-ft LED Tube — Low Bay (Type A/B/DM)", Amount: 3.00, Unit: "$/Lamp"},
			{ID: "LT107", Description: "4-ft T8 → One (1) 4-ft LED Tube — High Bay ≥15-ft (Type A/B/DM)", Amount: 4.00, Unit: "$/Lamp"},
			{ID: "LT108", Description: "4-ft T5 → One (1) 4-ft LED Tube — Low Bay (Type A/B/DM)", Amount: 3.00, Unit: "$/Lamp"},
			{ID: "LT109", Description: "4-ft T5 → One (1) 4-ft LED Tube — High Bay ≥15-ft (Type A/B/DM)", Amount: 4.00, Unit: "$/Lamp"},
			// 8-ft lamps
			{ID: "LT110", Description: "8-ft T12 → Two (2) 4-ft LED Tubes (Type A/B/DM)", Amount: 10.00, Unit: "$/8-ft Replaced"},
			{ID: "LT111", Description: "8-ft T8 → Two (2) 4-ft LED Tubes (Type A/B/DM)", Amount: 7.00, Unit: "$/8-ft Replaced"},
			{ID: "LT112", Description: "8-ft T12 → One (1) 8-ft LED Tube (Type A/B/DM)", Amount: 10.00, Unit: "$/8-ft Replaced"},
			{ID: "LT113", Description: "8-ft T8 → One (1) 8-ft LED Tube (Type A/B/DM)", Amount: 7.00, Unit: "$/8-ft Replaced"},
			// UL Type C — same amounts, different driver requirement
			{ID: "LT114", Description: "2-ft T12 → 2-ft LED Tube (Type C)", Amount: 2.50, Unit: "$/Lamp"},
			{ID: "LT115", Description: "2-ft T8 → 2-ft LED Tube (Type C)", Amount: 1.00, Unit: "$/Lamp"},
			{ID: "LT116", Description: "3-ft T12 → 3-ft LED Tube (Type C)", Amount: 3.50, Unit: "$/Lamp"},
			{ID: "LT117", Description: "3-ft T8 → 3-ft LED Tube (Type C)", Amount: 2.00, Unit: "$/Lamp"},
			{ID: "LT118", Description: "4-ft T12 → One (1) 4-ft LED Tube (Type C)", Amount: 5.00, Unit: "$/Lamp"},
			{ID: "LT119", Description: "4-ft T8 → One (1) 4-ft LED Tube — Low Bay (Type C)", Amount: 3.00, Unit: "$/Lamp"},
			{ID: "LT120", Description: "4-ft T8 → One (1) 4-ft LED Tube — High Bay ≥15-ft (Type C)", Amount: 4.00, Unit: "$/Lamp"},
			{ID: "LT121", Description: "4-ft T5 → One (1) 4-ft LED Tube — Low Bay (Type C)", Amount: 3.00, Unit: "$/Lamp"},
			{ID: "LT122", Description: "4-ft T5 → One (1) 4-ft LED Tube — High Bay ≥15-ft (Type C)", Amount: 4.00, Unit: "$/Lamp"},
			{ID: "LT123", Description: "8-ft T12 → Two (2) 4-ft LED Tubes (Type C)", Amount: 10.00, Unit: "$/8-ft Replaced"},
			{ID: "LT124", Description: "8-ft T8 → Two (2) 4-ft LED Tubes (Type C)", Amount: 7.00, Unit: "$/8-ft Replaced"},
			{ID: "LT125", Description: "8-ft T12 → One (1) 8-ft LED Tube (Type C)", Amount: 10.00, Unit: "$/8-ft Replaced"},
			{ID: "LT126", Description: "8-ft T8 → One (1) 8-ft LED Tube (Type C)", Amount: 7.00, Unit: "$/8-ft Replaced"},
			// LED Fixture and Lamp Retrofits (incentive per watt reduced)
			{ID: "LT207", Description: "Interior Linear LED Tube Light Fixtures — High Bay ≥15-ft", Amount: 0.55, Unit: "$/Watt Reduced"},
			{ID: "LT208", Description: "Interior Linear LED Tube Light Fixtures — High Bay ≥15-ft (Continuous Operation)", Amount: 1.00, Unit: "$/Watt Reduced"},
			{ID: "LT209", Description: "Interior Linear LED Tube Light Fixtures — Low Bay <15-ft", Amount: 0.30, Unit: "$/Watt Reduced"},
		},
	},

	// ── #3  Refrigeration Compressors — Discus or Scroll ──────────────────────
	{
		Key:         "refrigeration_compressors",
		MeasureName: "Discus or Scroll Compressors for Walk-In Coolers and Freezers",
		MeasureIDs:  "RL101, RL102",
		Category:    "Refrigeration, Laundry and Kitchen",
		Description: "Available for Consumers Energy electric customers installing high-efficiency " +
			"semi-hermetic discus (RL101) or scroll (RL102) compressors for walk-in coolers and freezers. " +
			"Incentive is based on nameplate (nominal) cooling capacity (tons); scroll compressors earn " +
			"a higher rate than discus. Pre-Notification required. Qualifies for New Construction and Retrofit. " +
			"Minimum eligible efficiencies per Tables 15.1 (low-temp) and 15.2 (medium-temp) in the Catalog.",
		CatalogPages: []PageRange{{Start: 85, End: 85}}, // printed p.83
		AppPages:     []PageRange{{Start: 36, End: 36}}, // printed p.36
		IncentiveRates: []ceRate{
			{ID: "RL101", Description: "Discus Compressors for Walk-In Coolers or Freezers", Amount: 20.00, Unit: "$/Ton"},
			{ID: "RL102", Description: "Scroll Compressors for Walk-In Coolers or Freezers", Amount: 40.00, Unit: "$/Ton"},
		},
	},
}

// ── Save options ──────────────────────────────────────────────────────────────

// PDFScrapeOpts controls database persistence for the PDF scraper.
type PDFScrapeOpts struct {
	// SaveSupabase upserts the raw extracted PDF text into pdf_scrape_raw.
	// Each extract is keyed on (source, measure_key, pdf_type), so re-running
	// always refreshes the text rather than appending a new row.
	// Optional — pass --save-supabase to enable.
	SaveSupabase bool

	// DB is the open database handle used to upsert into rebates_staging (always)
	// and optionally pdf_scrape_raw (when SaveSupabase is true).
	DB *db.DB

	// ScraperVersion is written to scraper_version in rebates_staging rows.
	// Defaults to "1.0" when empty.
	ScraperVersion string
}

// ── Incentive converter ───────────────────────────────────────────────────────

// ceToIncentive converts a ceIncentiveSpec into a single models.Incentive.
// All rate tiers are embedded in RateTiers — one program row, not one per tier.
// The deterministic ID is keyed on the measure key (e.g. "hvac_air_conditioning")
// so re-scraping always upserts the same row.
func ceToIncentive(spec ceIncentiveSpec, version string) models.Incentive {
	if version == "" {
		version = "1.0"
	}
	inc := models.NewIncentive("consumers_energy_pdf", version)
	inc.ID = models.DeterministicID("consumers_energy_pdf", spec.Key)
	inc.ProgramName = spec.MeasureName
	inc.UtilityCompany = "Consumers Energy"
	inc.Portfolio = []string{"Utility"}
	inc.State = models.PtrString("MI")
	inc.IncentiveDescription = models.PtrString(spec.Description)
	inc.IncentiveFormat = models.PtrString("tiered")
	inc.CategoryTag = []string{spec.Category}
	inc.ApplicationURL = models.PtrString("https://www.ConsumersEnergy.com/IncentiveApp")
	inc.ProgramURL = models.PtrString("https://www.ConsumersEnergy.com/StartSaving")
	inc.ContactEmail = models.PtrString("BusinessEnergyEfficiency@cmsenergy.com")
	inc.ContactPhone = models.PtrString("888-674-2770")
	inc.ApplicationProcess = models.PtrString("Pre-Notification required")
	inc.RateTiers = ceRatesToTiers(spec.IncentiveRates)
	return inc
}

// ceRatesToTiers converts []ceRate → []models.RateTier for JSON storage.
func ceRatesToTiers(rates []ceRate) []models.RateTier {
	out := make([]models.RateTier, len(rates))
	for i, r := range rates {
		out[i] = models.RateTier{
			ID:          r.ID,
			Description: r.Description,
			Amount:      r.Amount,
			Unit:        r.Unit,
		}
	}
	return out
}

// ── Scraper entry point ───────────────────────────────────────────────────────

// ScrapeConsumersEnergyPDFs reads the two local Consumers Energy PDFs, extracts
// the relevant pages for each of the three target incentive measures, and logs
// the structured incentive data.
//
// Rate tiers are always upserted into rebates_staging.
// Pass opts.SaveSupabase=true to also capture raw PDF text in pdf_scrape_raw.
//
// When LOG_FORMAT=console a human-readable, structured report is printed to
// stdout.  Otherwise structured JSON lines are emitted via zap.
//
// PDF file paths are resolved via environment variables:
//
//	CONSUMERS_ENERGY_CATALOG_PDF      (default: Consumers_Energy_Incentive_Catalog_1.pdf)
//	CONSUMERS_ENERGY_APPLICATION_PDF  (default: Incentive-Application.pdf)
func ScrapeConsumersEnergyPDFs(ctx context.Context, log *zap.Logger, opts PDFScrapeOpts) error {
	paths := cePaths()

	// Verify files exist before doing any work.
	if paths.Catalog == "" {
		return fmt.Errorf("catalog PDF path not provided — pass --catalog /path/to/catalog.pdf")
	}
	if paths.Application == "" {
		return fmt.Errorf("application PDF path not provided — pass --application /path/to/application.pdf")
	}
	if err := checkFile(paths.Catalog); err != nil {
		return fmt.Errorf("catalog PDF not found (%q): %w", paths.Catalog, err)
	}
	if err := checkFile(paths.Application); err != nil {
		return fmt.Errorf("application PDF not found (%q): %w", paths.Application, err)
	}

	// Choose output mode: pretty console report vs. structured JSON lines.
	if strings.ToLower(os.Getenv("LOG_FORMAT")) == "console" {
		return scrapeConsumersEnergyPretty(paths, log, opts)
	}
	return scrapeConsumersEnergyJSON(paths, log, opts)
}

// ── Console (pretty) output ───────────────────────────────────────────────────

const (
	lineWidth = 80
	thickLine = "━"
	thinLine  = "─"
	doubleLine = "═"
)

func ruler(char string) string { return strings.Repeat(char, lineWidth) }

// scrapeConsumersEnergyPretty prints a human-readable, structured report to
// stdout when LOG_FORMAT=console.
func scrapeConsumersEnergyPretty(paths cePDFPaths, log *zap.Logger, opts PDFScrapeOpts) error {
	p := fmt.Println
	pf := fmt.Printf

	// ── Header ────────────────────────────────────────────────────────────────
	p("")
	p(ruler(doubleLine))
	title := "  CONSUMERS ENERGY — 2026 Business Energy Efficiency Programs"
	p(title)
	p(ruler(doubleLine))
	pf("  %-20s %s\n", "Catalog PDF:", paths.Catalog)
	pf("  %-20s %s\n", "Application PDF:", paths.Application)
	pf("  %-20s %d\n", "Measures:", len(consumersEnergyIncentives))
	pf("  %-20s %s\n", "State:", "Michigan (MI)")
	pf("  %-20s %s\n", "Utility:", "Consumers Energy")
	pf("  %-20s %s\n", "Program Year:", "2026")
	pf("  %-20s %s\n", "Contact:", "BusinessEnergyEfficiency@cmsenergy.com  |  888-674-2770")
	pf("  %-20s %s\n", "Apply:", "https://www.ConsumersEnergy.com/IncentiveApp")
	p("")

	for i, spec := range consumersEnergyIncentives {

		// ── Measure heading ────────────────────────────────────────────────────
		p(ruler(thickLine))
		pf("  [%d/%d]  %s\n", i+1, len(consumersEnergyIncentives), spec.MeasureName)
		p(ruler(thickLine))
		pf("  %-20s %s\n", "Measure IDs:", spec.MeasureIDs)
		pf("  %-20s %s\n", "Category:", spec.Category)
		pf("  %-20s %s\n", "Catalog pages:", pageRangesToString(spec.CatalogPages))
		pf("  %-20s %s\n", "Application pages:", pageRangesToString(spec.AppPages))
		p("")

		// Description — word-wrapped at 76 chars
		p("  Description")
		p("  " + ruler(thinLine)[:40])
		for _, line := range wordWrap(spec.Description, 76) {
			pf("  %s\n", line)
		}
		p("")

		// ── Incentive rate table ───────────────────────────────────────────────
		pf("  Incentive Rates  (%d tiers)\n", len(spec.IncentiveRates))
		p("  " + ruler(thinLine)[:40])
		printRateTable(spec.IncentiveRates)
		p("")

		// ── Raw PDF text ───────────────────────────────────────────────────────
		// Catalog
		pf("  Raw Catalog Text  (PDF %s)\n", pageRangesToString(spec.CatalogPages))
		p("  " + ruler(thinLine)[:40])
		catalogText, err := ExtractLocalPDFPages(paths.Catalog, spec.CatalogPages)
		if err != nil {
			pf("  [extraction error: %v]\n", err)
		} else {
			printIndented(strings.TrimSpace(catalogText), "  ")
		}
		p("")

		// Application
		pf("  Raw Application Text  (PDF %s)\n", pageRangesToString(spec.AppPages))
		p("  " + ruler(thinLine)[:40])
		appText, err := ExtractLocalPDFPages(paths.Application, spec.AppPages)
		if err != nil {
			pf("  [extraction error: %v]\n", err)
		} else {
			printIndented(strings.TrimSpace(appText), "  ")
		}
		p("")
	}

	// ── Optional DB persistence ───────────────────────────────────────────────
	if err := ceSave(paths, opts, log); err != nil {
		return err
	}

	// ── Footer ────────────────────────────────────────────────────────────────
	p(ruler(doubleLine))
	note := "Staged to rebates_staging."
	if opts.SaveSupabase {
		note += "  Raw text saved to pdf_scrape_raw."
	} else {
		note += "  Use --save-supabase to also capture raw PDF text."
	}
	pf("  Done.  %d measures logged.  %s\n", len(consumersEnergyIncentives), note)
	p(ruler(doubleLine))
	p("")
	return nil
}

// printRateTable renders a fixed-width ASCII rate table to stdout.
func printRateTable(rates []ceRate) {
	// Column widths
	const colID = 8
	const colAmt = 8
	const colUnit = 18
	// description fills the rest: 80 - 2(indent) - colID - colAmt - colUnit - 3*3(sep+pad) = ~37
	const colDesc = 37

	border := func(l, m, r, h string) string {
		return "  " + l +
			strings.Repeat(h, colID+2) + m +
			strings.Repeat(h, colDesc+2) + m +
			strings.Repeat(h, colAmt+2) + m +
			strings.Repeat(h, colUnit+2) + r
	}

	row := func(id, desc, amt, unit string) string {
		// Truncate/pad description
		if len(desc) > colDesc {
			desc = desc[:colDesc-1] + "…"
		}
		return fmt.Sprintf("  │ %-*s │ %-*s │ %*s │ %-*s │",
			colID, id,
			colDesc, desc,
			colAmt, amt,
			colUnit, unit,
		)
	}

	fmt.Println(border("┌", "┬", "┐", "─"))
	fmt.Println(row("ID", "Description", "Amount", "Unit"))
	fmt.Println(border("├", "┼", "┤", "─"))
	for _, r := range rates {
		amt := fmt.Sprintf("$%.2f", r.Amount)
		fmt.Println(row(r.ID, r.Description, amt, r.Unit))
	}
	fmt.Println(border("└", "┴", "┘", "─"))
}

// printIndented prints each line of text with a leading indent.
func printIndented(text, indent string) {
	for _, line := range strings.Split(text, "\n") {
		fmt.Printf("%s%s\n", indent, line)
	}
}

// wordWrap breaks s into lines no longer than maxWidth characters.
func wordWrap(s string, maxWidth int) []string {
	words := strings.Fields(s)
	var lines []string
	var current strings.Builder
	for _, w := range words {
		if current.Len() == 0 {
			current.WriteString(w)
		} else if current.Len()+1+len(w) <= maxWidth {
			current.WriteByte(' ')
			current.WriteString(w)
		} else {
			lines = append(lines, current.String())
			current.Reset()
			current.WriteString(w)
		}
	}
	if current.Len() > 0 {
		lines = append(lines, current.String())
	}
	return lines
}

// ── JSON (structured) output ──────────────────────────────────────────────────

// scrapeConsumersEnergyJSON emits one structured zap log line per field — the
// default mode used by the daemon and log aggregators.
func scrapeConsumersEnergyJSON(paths cePDFPaths, log *zap.Logger, opts PDFScrapeOpts) error {
	sep := strings.Repeat("═", 78)

	log.Info("consumers_energy: starting",
		zap.String("catalog_pdf", paths.Catalog),
		zap.String("application_pdf", paths.Application),
		zap.Int("measures", len(consumersEnergyIncentives)),
	)

	for i, spec := range consumersEnergyIncentives {
		log.Info("consumers_energy: " + sep)
		log.Info("consumers_energy: measure",
			zap.Int("index", i+1),
			zap.Int("total", len(consumersEnergyIncentives)),
			zap.String("key", spec.Key),
			zap.String("measure_name", spec.MeasureName),
			zap.String("measure_ids", spec.MeasureIDs),
			zap.String("category", spec.Category),
			zap.String("source", "consumers_energy"),
			zap.String("state", "MI"),
			zap.String("utility_company", "Consumers Energy"),
			zap.String("program_year", "2026"),
			zap.String("pre_notification_required", "yes"),
			zap.String("eligible_customers", "Consumers Energy Electric Customers"),
			zap.String("application_url", "https://www.ConsumersEnergy.com/IncentiveApp"),
			zap.String("program_url", "https://www.ConsumersEnergy.com/StartSaving"),
			zap.String("contact_email", "BusinessEnergyEfficiency@cmsenergy.com"),
			zap.String("contact_phone", "888-674-2770"),
		)
		log.Info("consumers_energy: description",
			zap.String("key", spec.Key),
			zap.String("text", spec.Description),
		)

		log.Info("consumers_energy: incentive_rates",
			zap.String("key", spec.Key),
			zap.Int("rate_tiers", len(spec.IncentiveRates)),
		)
		for _, rate := range spec.IncentiveRates {
			log.Info("consumers_energy: rate",
				zap.String("measure_key", spec.Key),
				zap.String("id", rate.ID),
				zap.String("description", rate.Description),
				zap.Float64("amount", rate.Amount),
				zap.String("unit", rate.Unit),
			)
		}

		log.Info("consumers_energy: extracting_catalog_pages",
			zap.String("key", spec.Key),
			zap.String("pages", pageRangesToString(spec.CatalogPages)),
			zap.String("file", paths.Catalog),
		)
		catalogText, err := ExtractLocalPDFPages(paths.Catalog, spec.CatalogPages)
		if err != nil {
			log.Warn("consumers_energy: catalog extract failed", zap.String("key", spec.Key), zap.Error(err))
			catalogText = fmt.Sprintf("[extraction error: %v]", err)
		}
		log.Info("consumers_energy: catalog_text",
			zap.String("key", spec.Key),
			zap.String("pages", pageRangesToString(spec.CatalogPages)),
			zap.String("text", strings.TrimSpace(catalogText)),
		)

		log.Info("consumers_energy: extracting_application_pages",
			zap.String("key", spec.Key),
			zap.String("pages", pageRangesToString(spec.AppPages)),
			zap.String("file", paths.Application),
		)
		appText, err := ExtractLocalPDFPages(paths.Application, spec.AppPages)
		if err != nil {
			log.Warn("consumers_energy: application extract failed", zap.String("key", spec.Key), zap.Error(err))
			appText = fmt.Sprintf("[extraction error: %v]", err)
		}
		log.Info("consumers_energy: application_text",
			zap.String("key", spec.Key),
			zap.String("pages", pageRangesToString(spec.AppPages)),
			zap.String("text", strings.TrimSpace(appText)),
		)
	}

	log.Info("consumers_energy: " + sep)

	if err := ceSave(paths, opts, log); err != nil {
		return err
	}

	log.Info("consumers_energy: done",
		zap.Int("measures_logged", len(consumersEnergyIncentives)),
		zap.Bool("saved_raw", opts.SaveSupabase),
	)
	return nil
}

// ── Database persistence ──────────────────────────────────────────────────────

// ceSave writes to rebates_staging (always) and optionally to pdf_scrape_raw
// (when opts.SaveSupabase is true).  Called by both output modes so the logic lives
// in one place.
func ceSave(paths cePDFPaths, opts PDFScrapeOpts, log *zap.Logger) error {
	if opts.DB == nil {
		return fmt.Errorf("pdf-scraper: DB handle is nil — set DATABASE_URL in .env")
	}

	// ── staging (always) — one row per program, tiers embedded as JSON ────────
	var allIncentives []models.Incentive
	for _, spec := range consumersEnergyIncentives {
		allIncentives = append(allIncentives, ceToIncentive(spec, opts.ScraperVersion))
	}

	result, err := db.UpsertToStaging(opts.DB, allIncentives)
	if err != nil {
		return fmt.Errorf("consumers_energy: staging upsert: %w", err)
	}
	log.Info("consumers_energy: staging upsert complete",
		zap.String("table", "rebates_staging"),
		zap.Int("rows_upserted", result.Upserted),
		zap.Int("rate_tiers", len(allIncentives)),
	)

	// ── --save-supabase (optional) ─────────────────────────────────────────────────
	if opts.SaveSupabase {
		var entries []db.PDFRawEntry
		for _, spec := range consumersEnergyIncentives {
			// Catalog extract
			catalogText, err := ExtractLocalPDFPages(paths.Catalog, spec.CatalogPages)
			if err != nil {
				log.Warn("consumers_energy: raw catalog extract failed",
					zap.String("key", spec.Key), zap.Error(err))
				catalogText = fmt.Sprintf("[extraction error: %v]", err)
			}
			entries = append(entries, db.PDFRawEntry{
				Source:     "consumers_energy_pdf",
				MeasureKey: spec.Key,
				PDFType:    "catalog",
				Pages:      pageRangesToString(spec.CatalogPages),
				FilePath:   paths.Catalog,
				RawText:    strings.TrimSpace(catalogText),
			})

			// Application extract
			appText, err := ExtractLocalPDFPages(paths.Application, spec.AppPages)
			if err != nil {
				log.Warn("consumers_energy: raw application extract failed",
					zap.String("key", spec.Key), zap.Error(err))
				appText = fmt.Sprintf("[extraction error: %v]", err)
			}
			entries = append(entries, db.PDFRawEntry{
				Source:     "consumers_energy_pdf",
				MeasureKey: spec.Key,
				PDFType:    "application",
				Pages:      pageRangesToString(spec.AppPages),
				FilePath:   paths.Application,
				RawText:    strings.TrimSpace(appText),
			})
		}

		if err := db.UpsertPDFRaw(opts.DB, entries); err != nil {
			return fmt.Errorf("consumers_energy: raw upsert: %w", err)
		}
		log.Info("consumers_energy: raw upsert complete",
			zap.String("table", "pdf_scrape_raw"),
			zap.Int("rows_upserted", len(entries)),
		)
	}

	return nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func checkFile(path string) error {
	_, err := os.Stat(path)
	return err
}

// pageRangesToString formats a []PageRange for display (e.g. "p.11-12, p.50").
func pageRangesToString(ranges []PageRange) string {
	parts := make([]string, 0, len(ranges))
	for _, r := range ranges {
		if r.Start == r.End {
			parts = append(parts, fmt.Sprintf("p.%d", r.Start))
		} else {
			parts = append(parts, fmt.Sprintf("p.%d-%d", r.Start, r.End))
		}
	}
	return strings.Join(parts, ", ")
}
