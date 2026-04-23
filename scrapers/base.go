package scrapers

import (
	"context"
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
		all = append(all, items...)
	}

	return all
}
