// rehydrate_api.go — RehydrateStream for API-based scrapers (DSIRE, Energy Star).
//
// DSIRE: fetches only the states that already have programs in staging,
// then keeps only programs whose ID is already known.
//
// Energy Star: paginates the full API but skips incentives not in the
// staging DB (there is no per-ID endpoint).
package scrapers

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/incenva/rebate-scraper/models"
	"go.uber.org/zap"
)

// ── DSIRE USA ─────────────────────────────────────────────────────────────────

// RehydrateStream re-fetches only the states that have programs in staging
// and only keeps programs whose IDs were already staged.
func (s *DSIREScraper) RehydrateStream(ctx context.Context, records []RehydrateRecord, sink func([]models.Incentive)) error {
	// Build set of known program IDs from source_url.
	// source_url format: https://programs.dsireusa.org/system/program/detail/1036
	knownIDs := make(map[int]struct{}, len(records))
	for _, r := range records {
		if id := dsireIDFromURL(r.SourceURL); id > 0 {
			knownIDs[id] = struct{}{}
		}
	}

	// Collect distinct states present in staging.
	stateSet := make(map[string]struct{})
	for _, r := range records {
		if r.State != "" {
			stateSet[r.State] = struct{}{}
		}
	}
	states := make([]string, 0, len(stateSet))
	for st := range stateSet {
		states = append(states, st)
	}

	s.Logger.Info("dsireusa: rehydrating from staging",
		zap.Int("known_programs", len(knownIDs)),
		zap.Int("states", len(states)),
	)

	client := s.httpClient()
	seen := make(map[int]bool)

	for _, state := range states {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		programs, err := s.fetchState(ctx, client, state)
		if err != nil {
			s.Logger.Warn("dsireusa: rehydrate state failed",
				zap.String("state", state), zap.Error(err))
			continue
		}

		stateZIPs := s.StateZIPs[state]
		var batch []models.Incentive
		for _, p := range programs {
			if _, known := knownIDs[p.ID]; !known {
				continue // skip programs not in our staging DB
			}
			if seen[p.ID] {
				continue
			}
			seen[p.ID] = true
			inc := s.toIncentive(p, stateZIPs)
			if s.ScrapeDetails {
				detail := s.scrapeDetail(ctx, p.ID)
				s.applyDetail(&inc, detail)
			}
			batch = append(batch, inc)
		}
		if len(batch) > 0 {
			sink(batch)
			s.Logger.Info("dsireusa: rehydrated state",
				zap.String("state", state),
				zap.Int("programs", len(batch)),
			)
		}
		time.Sleep(s.pageDelay())
	}

	s.Logger.Info("dsireusa: rehydrate complete", zap.Int("programs", len(seen)))
	return nil
}

// dsireIDFromURL extracts the integer program ID from a DSIRE detail page URL.
// e.g. "https://programs.dsireusa.org/system/program/detail/1036" → 1036
func dsireIDFromURL(sourceURL string) int {
	if sourceURL == "" {
		return 0
	}
	parts := strings.Split(strings.TrimRight(sourceURL, "/"), "/")
	if len(parts) == 0 {
		return 0
	}
	id, _ := strconv.Atoi(parts[len(parts)-1])
	return id
}

// ── Energy Star ───────────────────────────────────────────────────────────────

// RehydrateStream paginates the Energy Star API and emits only incentives
// whose incentive_id is already in the staging DB.
// (The Energy Star API has no per-ID filter, so we must page through all results.)
func (s *EnergyStarScraper) RehydrateStream(ctx context.Context, records []RehydrateRecord, sink func([]models.Incentive)) error {
	// Build set of known incentive IDs from source_url.
	// source_url format: https://www.energystar.gov/rebate-finder?incentive_id=abc123
	knownIDs := make(map[string]struct{}, len(records))
	for _, r := range records {
		if id := energyStarIDFromURL(r.SourceURL); id != "" {
			knownIDs[id] = struct{}{}
		}
	}

	s.Logger.Info("energy_star: rehydrating from staging", zap.Int("known_ids", len(knownIDs)))

	version := s.ScraperVersion
	if version == "" {
		version = energyStarScraperVersion
	}

	// Probe page 0 to get total page count.
	probe, err := s.fetchPage(ctx, 0)
	if err != nil {
		return err
	}
	if probe.ResultsCount == 0 || probe.PageSize == 0 {
		s.Logger.Warn("energy_star: empty result set during rehydration")
		return nil
	}

	totalPages := probe.ResultsCount / probe.PageSize
	if probe.ResultsCount%probe.PageSize != 0 {
		totalPages++
	}

	seen := make(map[string]bool)

	flushFiltered := func(results []models.EnergyStarRawResult) {
		var batch []models.Incentive
		for _, result := range results {
			if _, known := knownIDs[result.IncentiveID]; !known {
				continue
			}
			inc, ok := mapEnergyStarRecord(result, version, s.StateZIPs, s.Logger)
			if !ok || seen[inc.ID] {
				continue
			}
			seen[inc.ID] = true
			batch = append(batch, inc)
		}
		if len(batch) > 0 {
			sink(batch)
		}
	}

	bar := NewProgressBar(totalPages, "energy_star")
	flushFiltered(probe.Results)
	bar.Add(1) //nolint:errcheck

	for page := 1; page < totalPages; page++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if s.pageDelay() > 0 {
			time.Sleep(s.pageDelay())
		}
		resp, fetchErr := s.fetchPage(ctx, page)
		if fetchErr != nil {
			s.Logger.Warn("energy_star: rehydrate page failed",
				zap.Int("page", page), zap.Error(fetchErr))
			bar.Add(1) //nolint:errcheck
			continue
		}
		flushFiltered(resp.Results)
		bar.Add(1) //nolint:errcheck
	}
	bar.Finish() //nolint:errcheck

	s.Logger.Info("energy_star: rehydrate complete", zap.Int("programs", len(seen)))
	return nil
}

// energyStarIDFromURL parses the incentive_id query param from a source URL.
// e.g. "https://www.energystar.gov/rebate-finder?incentive_id=abc" → "abc"
func energyStarIDFromURL(sourceURL string) string {
	if sourceURL == "" {
		return ""
	}
	idx := strings.Index(sourceURL, "incentive_id=")
	if idx < 0 {
		return ""
	}
	rest := sourceURL[idx+len("incentive_id="):]
	if amp := strings.Index(rest, "&"); amp >= 0 {
		rest = rest[:amp]
	}
	return rest
}
