package scrapers

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/incenva/rebate-scraper/models"
	"go.uber.org/zap"
)

// Scraper is the interface every source must implement.
// Name returns a stable identifier used in logs and the source column.
// Scrape fetches incentives and returns them as a flat slice.
type Scraper interface {
	Name() string
	Scrape(ctx context.Context) ([]models.Incentive, error)
}

// Rehydrater is implemented by scrapers that can re-fetch existing programs
// from their source using URLs/IDs already stored in the staging DB, instead
// of re-discovering the full program list from scratch.
//
// records contains one entry per existing staging row for this source.
// sink is called with each batch of freshly fetched incentives — same contract
// as StreamScraper.ScrapeStream.
type Rehydrater interface {
	Scraper
	RehydrateStream(ctx context.Context, records []RehydrateRecord, sink func([]models.Incentive)) error
}

// RehydrateRecord is the minimal staging row data a scraper needs to re-fetch
// one program from its source.
type RehydrateRecord struct {
	SourceID   string
	State      string // empty string when unknown
	ProgramURL string // may be empty
	SourceURL  string // may be empty
}

// StreamScraper is an optional interface for scrapers that can emit results
// incrementally. When implemented, RunListFlush calls ScrapeStream so each
// batch is upserted to the staging DB as soon as it is ready — no waiting
// for the full scrape to complete.
//
// Scrapers should call sink with the smallest natural batch (one state for
// DSIRE, one page for Energy Star, a worker-pool flush for Rewiring America).
// sink is safe to call from goroutines as long as the caller serialises access.
type StreamScraper interface {
	Scraper
	ScrapeStream(ctx context.Context, sink func([]models.Incentive)) error
}

// rehydrateURL returns the best URL to re-fetch for a given record:
// source_url if set, otherwise program_url.
func rehydrateURL(r RehydrateRecord) string {
	if r.SourceURL != "" {
		return r.SourceURL
	}
	return r.ProgramURL
}

// Registry holds all registered scrapers.
type Registry struct {
	scrapers []Scraper
}

// NewRegistry creates an empty Registry.
func NewRegistry() *Registry {
	return &Registry{}
}

// Register adds a scraper to the registry.
func (r *Registry) Register(s Scraper) {
	r.scrapers = append(r.scrapers, s)
}

// All returns a copy of the registered scrapers slice.
func (r *Registry) All() []Scraper {
	out := make([]Scraper, len(r.scrapers))
	copy(out, r.scrapers)
	return out
}

// Get returns the scraper registered under the given name, or nil if not found.
func (r *Registry) Get(name string) Scraper {
	for _, s := range r.scrapers {
		if s.Name() == name {
			return s
		}
	}
	return nil
}

// Names returns the names of all registered scrapers, in registration order.
func (r *Registry) Names() []string {
	names := make([]string, len(r.scrapers))
	for i, s := range r.scrapers {
		names[i] = s.Name()
	}
	return names
}

// RunList executes a specific slice of scrapers sequentially.
// Per-scraper errors are logged and do not abort the run.
func RunList(ctx context.Context, list []Scraper, logger *zap.Logger) []models.Incentive {
	var all []models.Incentive
	for _, s := range list {
		t0 := time.Now()
		logger.Info("scraper starting", zap.String("source", s.Name()))
		items, err := s.Scrape(ctx)
		if err != nil {
			logger.Error("scraper failed",
				zap.String("source", s.Name()),
				zap.Error(err),
				zap.Duration("elapsed", time.Since(t0)),
			)
			continue
		}
		logger.Info("scraper finished",
			zap.String("source", s.Name()),
			zap.Int("count", len(items)),
			zap.Duration("elapsed", time.Since(t0)),
		)
		logItemsDebug(logger, s.Name(), items)
		all = append(all, items...)
	}
	return all
}

// RunListFlush executes scrapers one at a time and calls flush as results
// arrive. Scrapers that implement StreamScraper flush per-batch (per state,
// per page, etc.); others flush once after all items are collected.
func RunListFlush(ctx context.Context, list []Scraper, logger *zap.Logger, flush func(source string, items []models.Incentive)) {
	for _, s := range list {
		t0 := time.Now()
		logger.Info("scraper starting", zap.String("source", s.Name()))

		if ss, ok := s.(StreamScraper); ok {
			// Streaming path: flush is called per batch as the scraper produces results.
			total := 0
			err := ss.ScrapeStream(ctx, func(batch []models.Incentive) {
				if len(batch) == 0 {
					return
				}
				logItemsDebug(logger, s.Name(), batch)
				flush(s.Name(), batch)
				total += len(batch)
			})
			if err != nil {
				logger.Error("scraper failed",
					zap.String("source", s.Name()),
					zap.Error(err),
					zap.Duration("elapsed", time.Since(t0)),
				)
				continue
			}
			logger.Info("scraper finished",
				zap.String("source", s.Name()),
				zap.Int("count", total),
				zap.Duration("elapsed", time.Since(t0)),
			)
		} else {
			// Batch path: collect all items, then flush once.
			items, err := s.Scrape(ctx)
			if err != nil {
				logger.Error("scraper failed",
					zap.String("source", s.Name()),
					zap.Error(err),
					zap.Duration("elapsed", time.Since(t0)),
				)
				continue
			}
			logger.Info("scraper finished",
				zap.String("source", s.Name()),
				zap.Int("count", len(items)),
				zap.Duration("elapsed", time.Since(t0)),
			)
			logItemsDebug(logger, s.Name(), items)
			if len(items) > 0 {
				flush(s.Name(), items)
			}
		}
	}
}

// logItemsDebug prints a compact table of scraped items to stdout.
// It is a no-op when the logger is not at debug level.
func logItemsDebug(logger *zap.Logger, source string, items []models.Incentive) {
	if !logger.Core().Enabled(zap.DebugLevel) || len(items) == 0 {
		return
	}

	fmt.Fprintf(os.Stdout, "\n  [DEBUG] %s — %d items\n\n", source, len(items))

	w := tabwriter.NewWriter(os.Stdout, 2, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  #\tPROGRAM\tSTATE\tFORMAT\tAMOUNT\t")
	fmt.Fprintln(w, "  "+strings.Repeat("─", 4)+"\t"+strings.Repeat("─", 46)+"\t"+strings.Repeat("─", 5)+"\t"+strings.Repeat("─", 16)+"\t"+strings.Repeat("─", 12)+"\t")

	for i, item := range items {
		state := "—"
		if item.State != nil && *item.State != "" {
			state = *item.State
		}

		format := "—"
		if item.IncentiveFormat != nil {
			format = *item.IncentiveFormat
		}

		amount := "—"
		switch {
		case item.IncentiveAmount != nil:
			amount = fmt.Sprintf("$%.0f", *item.IncentiveAmount)
		case item.PercentValue != nil:
			amount = fmt.Sprintf("%.0f%%", *item.PercentValue)
		case item.PerUnitAmount != nil && item.UnitType != nil:
			amount = fmt.Sprintf("$%.2f/%s", *item.PerUnitAmount, *item.UnitType)
		}

		name := item.ProgramName
		if len(name) > 46 {
			name = name[:43] + "..."
		}

		fmt.Fprintf(w, "  %d\t%s\t%s\t%s\t%s\t\n", i+1, name, state, format, amount)
	}
	w.Flush()
	fmt.Fprintln(os.Stdout)
}

// RunAll executes every registered scraper sequentially.
// Per-scraper errors are logged and do not abort the overall run —
// partial results from successful scrapers are still returned.
func RunAll(ctx context.Context, reg *Registry, logger *zap.Logger) []models.Incentive {
	var all []models.Incentive

	for _, s := range reg.All() {
		t0 := time.Now()
		logger.Info("scraper starting", zap.String("source", s.Name()))

		items, err := s.Scrape(ctx)
		if err != nil {
			logger.Error("scraper failed",
				zap.String("source", s.Name()),
				zap.Error(err),
				zap.Duration("elapsed", time.Since(t0)),
			)
			continue
		}

		logger.Info("scraper finished",
			zap.String("source", s.Name()),
			zap.Int("count", len(items)),
			zap.Duration("elapsed", time.Since(t0)),
		)
		logItemsDebug(logger, s.Name(), items)
		all = append(all, items...)
	}

	return all
}
