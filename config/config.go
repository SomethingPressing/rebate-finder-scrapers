package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// Config holds all configuration for the scraper service.
// Values are read from environment variables; defaults are applied where sensible.
type Config struct {
	// DatabaseURL is the PostgreSQL connection string (shared with the Next.js app).
	DatabaseURL string

	// ScraperInterval is a robfig/cron schedule string, e.g. "@every 6h" or "0 2 * * *".
	ScraperInterval string

	// DSIREUSA
	DSIREBaseURL string

	// Rewiring America
	RewiringAmericaAPIKey  string
	RewiringAmericaBaseURL string

	// Energy Star
	EnergyStarBaseURL string

	// ScraperVersion is written to the scraper_version column on every upsert.
	ScraperVersion string

	// LogLevel controls zap verbosity: "debug", "info", "warn", "error".
	LogLevel string

	// LogFormat is "json" (default) or "console" (human-readable lines on stdout).
	LogFormat string

	// RunOnce: when true, run all scrapers once and exit (useful for CI / manual runs).
	RunOnce bool

	// Source, when non-empty, restricts the run to a single scraper by name.
	// Valid values: "dsireusa", "rewiring_america", "energy_star".
	// Takes precedence over the --source CLI flag (env var wins if both are set).
	Source string

}

// Load reads configuration from the environment.
// It silently ignores a missing .env file so Docker env vars work without one.
//
// Dotenv loading (in order):
//   1. If DOTENV_PATH is set, load that file only.
//   2. Otherwise walk upward from the process working directory until a directory
//      contains both prisma/schema.prisma and .env (monorepo root), then load .env.
//
// No scraper-service/.env is required; keep a single .env at the repo root.
func Load() (*Config, error) {
	loadDotenv()

	cfg := &Config{
		DatabaseURL:            getEnv("DATABASE_URL", ""),
		ScraperInterval:        getEnv("SCRAPER_INTERVAL", "@every 6h"),
		DSIREBaseURL:           getEnv("DSIREUSA_BASE_URL", "https://programs.dsireusa.org/api/v1/programs"),
		RewiringAmericaAPIKey:  getEnv("REWIRING_AMERICA_API_KEY", ""),
		RewiringAmericaBaseURL: getEnv("REWIRING_AMERICA_BASE_URL", "https://api.rewiringamerica.org/api/v1/calculator"),
		EnergyStarBaseURL:      getEnv("ENERGY_STAR_BASE_URL", "https://www.energystar.gov/about/federal_tax_credits"),
		ScraperVersion:         getEnv("SCRAPER_VERSION", "1.0"),
		LogLevel:               getEnv("LOG_LEVEL", "info"),
		LogFormat:              getEnv("LOG_FORMAT", "json"),
		RunOnce: getBoolEnv("RUN_ONCE", false),
		Source:  getEnv("SOURCE", ""),
	}

	return cfg, nil
}

// loadDotenv populates os.Environ from a .env file.
//
// Loading order (first match wins):
//  1. DOTENV_PATH env var — load that exact file.
//  2. Walk upward from the working directory until a directory contains
//     both go.mod and .env — that .env is loaded.
//
// This works for both standalone repos (go.mod + .env at root) and monorepo
// usage (go.mod + .env inside scraper-service/ when cwd is set there by the
// runner script).  If neither is found the process continues with whatever
// vars are already in the environment (e.g. Docker / CI env vars).
func loadDotenv() {
	if p := strings.TrimSpace(os.Getenv("DOTENV_PATH")); p != "" {
		_ = godotenv.Load(p)
		return
	}

	dir, err := os.Getwd()
	if err != nil {
		return
	}

	search := dir
	for range 20 {
		goMod := filepath.Join(search, "go.mod")
		envFile := filepath.Join(search, ".env")
		if fileExists(goMod) && fileExists(envFile) {
			_ = godotenv.Load(envFile)
			return
		}
		parent := filepath.Dir(search)
		if parent == search {
			break
		}
		search = parent
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func getBoolEnv(key string, defaultVal bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return defaultVal
	}
	return b
}
