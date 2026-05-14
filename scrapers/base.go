package scrapers

import (
	"context"
	"fmt"
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

// RunListFlush executes scrapers one at a time and calls flush immediately
// after each one finishes, rather than buffering all results until the full
// run completes. This prevents data loss when a later scraper fails — each
// scraper's rows are persisted as soon as they are fetched.
func RunListFlush(ctx context.Context, list []Scraper, logger *zap.Logger, flush func(source string, items []models.Incentive)) {
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
		if len(items) > 0 {
			flush(s.Name(), items)
		}
	}
}

// logItemsDebug emits one zap.Debug entry per scraped item with all key fields
// and a raw-response preview. These calls are no-ops when the logger level is
// higher than debug, so there is no performance cost in production runs.
func logItemsDebug(logger *zap.Logger, source string, items []models.Incentive) {
	for i, item := range items {
		logger.Debug("scraped item",
			zap.String("source", source),
			zap.Int("index", i),
			zap.String("program_name", item.ProgramName),
			zap.String("utility_company", item.UtilityCompany),
			zap.Stringp("state", item.State),
			zap.Stringp("incentive_format", item.IncentiveFormat),
			zap.Float64p("incentive_amount", item.IncentiveAmount),
			zap.Float64p("maximum_amount", item.MaximumAmount),
			zap.Float64p("percent_value", item.PercentValue),
			zap.Float64p("per_unit_amount", item.PerUnitAmount),
			zap.Stringp("unit_type", item.UnitType),
			zap.Strings("category_tags", item.CategoryTag),
			zap.Stringp("customer_type", item.CustomerType),
			zap.Stringp("service_territory", item.ServiceTerritory),
			zap.Stringp("program_url", item.ProgramURL),
			zap.Stringp("application_url", item.ApplicationURL),
			zap.Stringp("contact_email", item.ContactEmail),
			zap.Stringp("contact_phone", item.ContactPhone),
			zap.Stringp("start_date", item.StartDate),
			zap.Stringp("end_date", item.EndDate),
			zap.Int("raw_response_bytes", len(item.RawResponse)),
			zap.String("raw_content_type", item.RawContentType),
		)

		if item.RawResponse != "" && logger.Core().Enabled(zap.DebugLevel) {
			preview := item.RawResponse
			if len(preview) > 1000 {
				preview = preview[:1000] + fmt.Sprintf(" ... [%d more bytes]", len(item.RawResponse)-1000)
			}
			logger.Debug("raw response",
				zap.String("source", source),
				zap.String("program_name", item.ProgramName),
				zap.String("content_type", item.RawContentType),
				zap.String("raw_response", preview),
			)
		}
	}
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
