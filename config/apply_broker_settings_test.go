package config

import (
	"testing"

	"go.uber.org/zap"
)

// The settings screen in the broker console wrote a table nothing read. These
// tests pin the overlay that finally reads it, and in particular the rule that
// makes the whole thing safe to deploy: a broker with nothing to say must leave
// the collector running on exactly what it already had.

func settings(pairs map[string]string) *CollectorSettings {
	return &CollectorSettings{values: pairs}
}

func baseConfig() *Config {
	return &Config{
		MaxConcurrency:             3,
		RewiringAmericaConcurrency: 3,
		RewiringAmericaAPIKey:      "from-env",
	}
}

func TestApplyBrokerSettingsOverlaysConcurrency(t *testing.T) {
	cfg := baseConfig()

	applied := cfg.ApplyBrokerSettings(settings(map[string]string{
		key(FleetScope, "SCRAPER_CONCURRENCY"): "6",
	}), zap.NewNop())

	if cfg.MaxConcurrency != 6 {
		t.Fatalf("concurrency = %d, want 6", cfg.MaxConcurrency)
	}
	if len(applied) != 1 || applied[0] != "SCRAPER_CONCURRENCY" {
		t.Fatalf("applied = %v, want [SCRAPER_CONCURRENCY]", applied)
	}
}

func TestApplyBrokerSettingsIsScopedPerSource(t *testing.T) {
	// Scope is "*" or a source name. A fleet-wide value must not leak into a
	// source's own setting, or switching one collector's concurrency would
	// quietly change every other collector's too.
	cfg := baseConfig()

	cfg.ApplyBrokerSettings(settings(map[string]string{
		key("rewiring_america", "REWIRING_AMERICA_CONCURRENCY"): "8",
	}), zap.NewNop())

	if cfg.RewiringAmericaConcurrency != 8 {
		t.Fatalf("rewiring concurrency = %d, want 8", cfg.RewiringAmericaConcurrency)
	}
	if cfg.MaxConcurrency != 3 {
		t.Fatalf("fleet concurrency changed to %d; a source setting must not touch it", cfg.MaxConcurrency)
	}
}

func TestApplyBrokerSettingsLeavesTheEnvironmentAloneWhenSilent(t *testing.T) {
	// The property that makes this safe to ship: a broker mid-migration, or a
	// setting nobody has filled in, leaves the fleet exactly as it was. Starting
	// a collector with nothing because a table was empty would be far worse than
	// never having read it.
	cfg := baseConfig()

	applied := cfg.ApplyBrokerSettings(settings(map[string]string{}), zap.NewNop())

	if len(applied) != 0 {
		t.Fatalf("applied = %v, want nothing", applied)
	}
	if cfg.MaxConcurrency != 3 || cfg.RewiringAmericaConcurrency != 3 || cfg.RewiringAmericaAPIKey != "from-env" {
		t.Fatal("an empty settings table changed the running configuration")
	}
}

func TestApplyBrokerSettingsSurvivesNoBrokerAtAll(t *testing.T) {
	cfg := baseConfig()

	if applied := cfg.ApplyBrokerSettings(nil, zap.NewNop()); applied != nil {
		t.Fatalf("applied = %v, want nil", applied)
	}
	if cfg.MaxConcurrency != 3 {
		t.Fatal("a nil settings set changed the running configuration")
	}
}

func TestApplyBrokerSettingsReportsNothingWhenTheValueMatches(t *testing.T) {
	// "Applied" means "changed something". Reporting a setting that already
	// matched would put a line in the log implying the broker had an effect it
	// did not have — which is the same class of lie this whole change removes.
	cfg := baseConfig()

	applied := cfg.ApplyBrokerSettings(settings(map[string]string{
		key(FleetScope, "SCRAPER_CONCURRENCY"): "3",
	}), zap.NewNop())

	if len(applied) != 0 {
		t.Fatalf("applied = %v, want nothing for an unchanged value", applied)
	}
}

func TestApplyBrokerSettingsIgnoresRubbish(t *testing.T) {
	// Someone typing into the console, or a value written straight into the
	// table. A collector must not start with zero concurrency because of it.
	cfg := baseConfig()

	cfg.ApplyBrokerSettings(settings(map[string]string{
		key(FleetScope, "SCRAPER_CONCURRENCY"): "not a number",
	}), zap.NewNop())

	if cfg.MaxConcurrency != 3 {
		t.Fatalf("concurrency = %d; a malformed value must leave it alone", cfg.MaxConcurrency)
	}
}

func TestApplyBrokerSettingsTakesAnAPIKey(t *testing.T) {
	// Without it that collector logs a warning and skips itself entirely, so
	// this is the setting whose absence looks like a broken scraper.
	cfg := baseConfig()

	applied := cfg.ApplyBrokerSettings(settings(map[string]string{
		key("rewiring_america", "REWIRING_AMERICA_API_KEY"): "from-broker",
	}), zap.NewNop())

	if cfg.RewiringAmericaAPIKey != "from-broker" {
		t.Fatalf("api key = %q, want the broker's", cfg.RewiringAmericaAPIKey)
	}
	if len(applied) != 1 {
		t.Fatalf("applied = %v, want one entry", applied)
	}
}

func TestApplyBrokerSettingsNeverBlanksAKeyItDoesNotHave(t *testing.T) {
	// An empty broker value must not erase a working key from the environment.
	// That would take a collector offline to apply a setting nobody set.
	cfg := baseConfig()

	cfg.ApplyBrokerSettings(settings(map[string]string{
		key("rewiring_america", "REWIRING_AMERICA_API_KEY"): "",
	}), zap.NewNop())

	if cfg.RewiringAmericaAPIKey != "from-env" {
		t.Fatalf("api key = %q; an empty broker value must not erase it", cfg.RewiringAmericaAPIKey)
	}
}
