package config

import "go.uber.org/zap"

// ApplyBrokerSettings overlays what the broker had to say onto this run's
// config, and returns the names of the ones that actually changed something.
//
// Returning the names rather than logging inside is deliberate: "the broker set
// SCRAPER_CONCURRENCY to 4" is the line that proves the wiring works, and it
// belongs in one place next to the values it changed rather than scattered.
//
// SCRAPER_INTERVAL_HOURS is read but NOT applied here, and the reason is worth
// stating: under RUN_ONCE the process exits before any schedule is installed,
// so a cadence setting has nowhere to land. It is applied in scheduled mode
// only, below, where a cron actually exists to receive it.
func (cfg *Config) ApplyBrokerSettings(settings *CollectorSettings, logger *zap.Logger) []string {
	if settings == nil {
		return nil
	}
	var applied []string

	if n := settings.Int(FleetScope, "SCRAPER_CONCURRENCY", "MAX_CONCURRENCY", cfg.MaxConcurrency); n != cfg.MaxConcurrency {
		logger.Info("broker set concurrency", zap.Int("was", cfg.MaxConcurrency), zap.Int("now", n))
		cfg.MaxConcurrency = n
		applied = append(applied, "SCRAPER_CONCURRENCY")
	}

	if n := settings.Int("rewiring_america", "REWIRING_AMERICA_CONCURRENCY", "REWIRING_AMERICA_CONCURRENCY", cfg.RewiringAmericaConcurrency); n != cfg.RewiringAmericaConcurrency {
		logger.Info("broker set rewiring_america concurrency",
			zap.Int("was", cfg.RewiringAmericaConcurrency), zap.Int("now", n))
		cfg.RewiringAmericaConcurrency = n
		applied = append(applied, "REWIRING_AMERICA_CONCURRENCY")
	}

	// The key is never logged, only the fact that one arrived. A collector with
	// no key skips itself entirely, so "did the broker supply it?" is a question
	// somebody will ask while looking at an empty run.
	if k := settings.String("rewiring_america", "REWIRING_AMERICA_API_KEY", "REWIRING_AMERICA_API_KEY"); k != "" && k != cfg.RewiringAmericaAPIKey {
		cfg.RewiringAmericaAPIKey = k
		applied = append(applied, "REWIRING_AMERICA_API_KEY")
	}

	return applied
}
