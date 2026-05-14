package scrapers

import (
	"encoding/json"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/incenva/rebate-scraper/models"
	"go.uber.org/zap"
)

// Reextract runs the current extraction logic on already-fetched raw content
// and returns a fresh models.Incentive reflecting the current scraper code.
//
// It is used by the evaluator so quality scores always reflect the latest
// extraction logic rather than stale values cached in rebates_staging.
//
// source        — scraper name, e.g. "dsireusa", "con_edison"
// content       — raw page body (HTML or JSON) as a string
// contentType   — MIME type, e.g. "text/html" or "application/json"
// pageURL       — the program's web URL (used as ID + context for HTML scrapers)
// scraperVersion — written to scraper_version column; pass "" to skip
// stateZIPs     — US ZIP dataset; only required for DSIRE and Energy Star
func Reextract(
	source, content, contentType, pageURL, scraperVersion string,
	stateZIPs map[string][]string,
) *models.Incentive {
	switch source {
	case "dsireusa":
		return reextractDSIRE(content, scraperVersion, stateZIPs)
	case "Energy Star", "energy_star":
		return reextractEnergyStar(content, scraperVersion, stateZIPs)
	default:
		return reextractHTML(source, content, pageURL, scraperVersion)
	}
}

// reextractDSIRE deserialises the stored API JSON and re-runs toIncentive so
// every code fix (format detection, category normalisation, HTML entity decoding)
// is reflected without re-calling the DSIRE API.
func reextractDSIRE(jsonContent, version string, stateZIPs map[string][]string) *models.Incentive {
	var p dsireProgram
	if err := json.Unmarshal([]byte(jsonContent), &p); err != nil {
		return nil
	}
	if stateZIPs == nil {
		stateZIPs = make(map[string][]string)
	}
	s := &DSIREScraper{ScraperVersion: version, StateZIPs: stateZIPs}
	inc := s.toIncentive(p, stateZIPs[p.StateObj.Abbreviation])
	return &inc
}

// reextractEnergyStar deserialises the stored API JSON and re-runs the Energy
// Star mapping so the latest field-mapping logic is applied.
func reextractEnergyStar(jsonContent, version string, stateZIPs map[string][]string) *models.Incentive {
	var r models.EnergyStarRawResult
	if err := json.Unmarshal([]byte(jsonContent), &r); err != nil {
		return nil
	}
	if stateZIPs == nil {
		stateZIPs = make(map[string][]string)
	}
	inc, ok := mapEnergyStarRecord(r, version, stateZIPs, zap.NewNop())
	if !ok {
		return nil
	}
	return &inc
}

// reextractHTML parses the HTML with goquery and runs ExtractPageGoquery using
// the per-source PageExtractConfig, mirroring the live scrape path exactly.
func reextractHTML(source, htmlContent, pageURL, version string) *models.Incentive {
	base := htmlExtractCfgForSource(source)
	if base == nil {
		return nil
	}
	base.ScraperVersion = version

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlContent))
	if err != nil {
		return nil
	}
	return ExtractPageGoquery(doc, pageURL, *base)
}

// htmlExtractCfgForSource returns a copy of the per-scraper PageExtractConfig.
// Returns nil when the source is unknown (API-based sources handled elsewhere).
func htmlExtractCfgForSource(source string) *PageExtractConfig {
	switch source {
	case "con_edison":
		c := conEdisonExtractCfg
		return &c
	case "pnm":
		c := pnmExtractCfg
		return &c
	case "xcel_energy":
		c := xcelExtractCfg
		return &c
	case "srp":
		c := srpExtractCfg
		return &c
	case "peninsula_clean_energy":
		c := pceExtractCfg
		return &c
	default:
		return nil
	}
}
