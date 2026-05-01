// extract_goquery.go — shared goquery-based page extraction.
//
// ExtractPageGoquery mirrors the Colly-based extractPage() methods across all
// five HTML scrapers.  It is used by:
//   - The headless-browser path (SRP, always; others when triggered).
//   - The 403/permission fallback path in Colly-based scrapers.
//
// Every scraper defines a package-level PageExtractConfig with its constants.
// At scrape-time the caller copies it and fills in ScraperVersion from config.
package scrapers

import (
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/incenva/rebate-scraper/models"
)

// PageExtractConfig parameterises the shared extraction logic for a single
// HTML scraper.  All fields except ScraperVersion, StateDetector, and
// AmountSelectors are typically set in a package-level constant.
type PageExtractConfig struct {
	Source         string
	ScraperVersion string
	UtilityCompany string

	// State / ZipCode / Territory are hardcoded for single-state scrapers.
	// Leave empty for multi-state scrapers that use StateDetector instead.
	State     string
	ZipCode   string
	Territory string

	DefaultApply string

	// BaseURL is used to resolve relative hrefs to absolute, e.g.
	// "https://www.srpnet.com" so "/rebates" → "https://www.srpnet.com/rebates".
	BaseURL string

	// SkipPhrases are lower-case substrings that, when found in the page
	// title, mark the page as non-programme content.  Defaults to a sensible
	// set covering 404/error/home/login pages.
	SkipPhrases []string

	// AmountSelectors extends the default "p, li, td, h2, h3" CSS selector
	// used when scanning individual elements for incentive amounts.
	// Example: "strong" (Xcel Energy uses <strong> tags for dollar amounts).
	AmountSelectors string

	// StateDetector is called with the concatenation of pageURL and pageText
	// for multi-state scrapers (Xcel Energy) that need to infer state,
	// territory, and representative ZIP from page content.
	// Returns empty strings when the state cannot be determined.
	StateDetector func(text string) (state, territory, zip string)
}

// ExtractPageGoquery extracts a single models.Incentive from a goquery
// document using the same two-pass logic (title → description → amounts →
// application URL → structured helpers) as the Colly-based extractPage
// methods.  Returns nil if the page does not look like an incentive programme.
func ExtractPageGoquery(doc *goquery.Document, pageURL string, cfg PageExtractConfig) *models.Incentive {
	// ── Programme name ────────────────────────────────────────────────────���───
	programName := strings.TrimSpace(doc.Find("h1").First().Text())
	if programName == "" {
		programName = strings.TrimSpace(doc.Find("title").First().Text())
		if idx := strings.Index(programName, "|"); idx > 0 {
			programName = strings.TrimSpace(programName[:idx])
		}
		if idx := strings.Index(programName, " - "); idx > 0 {
			programName = strings.TrimSpace(programName[:idx])
		}
	}
	if programName == "" || len(programName) < 5 {
		return nil
	}

	skipPhrases := cfg.SkipPhrases
	if len(skipPhrases) == 0 {
		skipPhrases = []string{"page not found", "404", "error", "home", "login", "site map"}
	}
	titleLower := strings.ToLower(programName)
	for _, p := range skipPhrases {
		if strings.Contains(titleLower, p) {
			return nil
		}
	}

	// ── Description ───────────────────────────────────────────────────────────
	description, _ := doc.Find(`meta[name="description"]`).Attr("content")
	description = strings.TrimSpace(description)
	if description == "" {
		doc.Find("p").EachWithBreak(func(_ int, s *goquery.Selection) bool {
			t := strings.TrimSpace(s.Text())
			if len(t) > 40 {
				description = t
				return false
			}
			return true
		})
	}
	if description == "" {
		description = programName
	}
	if len(description) > 500 {
		description = description[:497] + "..."
	}

	// ── Full page text (feeds all regex helpers) ──────────────────────────────
	pageText := doc.Find("html").Text()

	// ── State / territory / zip ───────────────────────────────────────────────
	state := cfg.State
	territory := cfg.Territory
	zip := cfg.ZipCode
	if cfg.StateDetector != nil {
		if ds, dt, dz := cfg.StateDetector(pageURL + " " + pageText); ds != "" {
			state = ds
			territory = dt
			zip = dz
		}
	}

	// ── Amount parsing ────────────────────────────────────────────────────────
	amtSel := "p, li, td, h2, h3"
	if cfg.AmountSelectors != "" {
		amtSel += ", " + cfg.AmountSelectors
	}

	format, amount := ParseAmount(pageText)
	if format == "narrative" {
		doc.Find(amtSel).EachWithBreak(func(_ int, s *goquery.Selection) bool {
			f, a := ParseAmount(s.Text())
			if f != "narrative" {
				format = f
				amount = a
				return false
			}
			return true
		})
	}

	var maxAmount *float64
	if format == "dollar_amount" {
		_, upToAmt := ParseAmount(pageText)
		if upToAmt != nil && amount != nil && *upToAmt > *amount {
			maxAmount = upToAmt
		}
	}

	// ── Application URL ───────────────────────────────────────────────────────
	applicationURL := ""
	doc.Find("a[href]").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		href, _ := s.Attr("href")
		text := strings.ToLower(s.Text() + " " + href)
		if strings.Contains(text, "apply") || strings.Contains(text, "application") ||
			strings.Contains(text, "enroll") || strings.Contains(text, "sign up") ||
			strings.Contains(text, "submit") {
			if strings.HasPrefix(href, "http") {
				applicationURL = href
				return false
			} else if strings.HasPrefix(href, "/") && cfg.BaseURL != "" {
				applicationURL = cfg.BaseURL + href
				return false
			}
		}
		return true
	})

	// ── Structured helpers (all string-based, from html_helpers.go) ──────────
	contractorRequired := extractContractorRequired(pageText)
	energyAuditRequired := extractEnergyAuditRequired(pageText)
	customerType := extractCustomerType(pageURL + " " + programName)
	startDate := extractStartDate(pageText)
	endDate := extractEndDate(pageText)
	contactPhone := extractPhone(pageText)
	contactEmail := extractEmail(pageText)
	categories := inferCategories(pageURL + " " + titleLower)

	if format == "" {
		format = "narrative"
	}
	id := models.DeterministicID(cfg.Source, pageURL)

	inc := models.NewIncentive(cfg.Source, cfg.ScraperVersion)
	inc.ID = id
	inc.ProgramName = programName
	inc.UtilityCompany = cfg.UtilityCompany
	inc.IncentiveDescription = models.PtrString(description)
	inc.IncentiveFormat = models.PtrString(format)
	inc.ApplicationProcess = models.PtrString(cfg.DefaultApply)
	inc.ProgramURL = models.PtrString(pageURL)
	inc.AvailableNationwide = models.PtrBool(false)
	inc.CategoryTag = categories
	inc.ProgramHash = models.ComputeProgramHash(programName, cfg.UtilityCompany)

	if state != "" {
		inc.State = models.PtrString(state)
	}
	if zip != "" {
		inc.ZipCode = models.PtrString(zip)
	}
	if territory != "" {
		inc.ServiceTerritory = models.PtrString(territory)
	}
	if amount != nil {
		inc.IncentiveAmount = amount
	}
	if maxAmount != nil {
		inc.MaximumAmount = maxAmount
	}
	if applicationURL != "" {
		inc.ApplicationURL = models.PtrString(applicationURL)
	}
	if contactPhone != "" {
		inc.ContactPhone = models.PtrString(contactPhone)
	}
	if contactEmail != "" {
		inc.ContactEmail = models.PtrString(contactEmail)
	}
	if contractorRequired != nil {
		inc.ContractorRequired = contractorRequired
	}
	if energyAuditRequired != nil {
		inc.EnergyAuditRequired = energyAuditRequired
	}
	if customerType != "" {
		inc.CustomerType = models.PtrString(customerType)
	}
	if startDate != "" {
		inc.StartDate = models.PtrString(startDate)
	}
	if endDate != "" {
		inc.EndDate = models.PtrString(endDate)
	}

	return &inc
}

// isPermissionError returns true when the error from FetchSitemapURLs looks
// like an HTTP 403/401/407 response — i.e. the server actively rejected the
// request based on IP or credentials rather than a network error.
func isPermissionError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "HTTP 403") ||
		strings.Contains(msg, "HTTP 401") ||
		strings.Contains(msg, "HTTP 407")
}
