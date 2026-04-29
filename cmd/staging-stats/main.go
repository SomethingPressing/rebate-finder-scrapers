// Command staging-stats prints an analytics report for the rebates_staging table.
//
// Usage:
//
//	./staging-stats
//	DATABASE_URL=postgres://... ./staging-stats
//	./staging-stats --json        # machine-readable output
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/incenva/rebate-scraper/config"
	"github.com/incenva/rebate-scraper/db"
	"github.com/incenva/rebate-scraper/models"
)

// ── US states list (loaded from data/us_states.json) ─────────────────────────

type usState struct {
	Abbr string `json:"abbr"`
	Name string `json:"name"`
}

// loadUSStates reads data/us_states.json, searching upward from the binary
// and from the source file location so it works both when installed and in
// development (go run ./cmd/staging-stats).
func loadUSStates() ([]usState, error) {
	candidates := []string{
		"data/us_states.json",
		"../data/us_states.json",
		"../../data/us_states.json",
	}
	// Also try relative to this source file (works with go run in sub-packages).
	_, thisFile, _, ok := runtime.Caller(0)
	if ok {
		dir := filepath.Dir(thisFile)
		for i := 0; i < 4; i++ {
			candidates = append(candidates, filepath.Join(dir, "data/us_states.json"))
			dir = filepath.Dir(dir)
		}
	}

	for _, c := range candidates {
		f, err := os.Open(c)
		if err != nil {
			continue
		}
		defer f.Close()
		var states []usState
		if err := json.NewDecoder(f).Decode(&states); err != nil {
			return nil, fmt.Errorf("staging-stats: parse %s: %w", c, err)
		}
		return states, nil
	}
	return nil, fmt.Errorf("staging-stats: data/us_states.json not found")
}

// ── Query result structs ──────────────────────────────────────────────────────

type statusRow struct {
	Status string `gorm:"column:status"`
	Count  int64  `gorm:"column:count"`
}

type sourceRow struct {
	Source   string  `gorm:"column:source"`
	Total    int64   `gorm:"column:total"`
	Pending  int64   `gorm:"column:pending"`
	Promoted int64   `gorm:"column:promoted"`
	Skipped  int64   `gorm:"column:skipped"`
	PctProm  float64 `gorm:"column:pct_promoted"`
}

type formatRow struct {
	Format string `gorm:"column:format"`
	Count  int64  `gorm:"column:count"`
}

// StateCount pairs a state entry from the hardcoded list with its DB count.
type StateCount struct {
	Abbr  string `json:"abbr"`
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

type qualityRow struct {
	NoDescription int64 `gorm:"column:no_description"`
	NoAmount      int64 `gorm:"column:no_amount"`
	NoState       int64 `gorm:"column:no_state"`
	NoURL         int64 `gorm:"column:no_url"`
	NoServiceArea int64 `gorm:"column:no_service_area"`
}

type activityRow struct {
	Period  string `gorm:"column:period"`
	Added   int64  `gorm:"column:added"`
	Updated int64  `gorm:"column:updated"`
}

// dbStateRow is used only to scan the GROUP BY result from the DB.
type dbStateRow struct {
	State string `gorm:"column:state"`
	Count int64  `gorm:"column:count"`
}

// ── Report ────────────────────────────────────────────────────────────────────

type Report struct {
	GeneratedAt string       `json:"generated_at"`
	Total       int64        `json:"total"`
	ByStatus    []statusRow  `json:"by_status"`
	BySource    []sourceRow  `json:"by_source"`
	ByFormat    []formatRow  `json:"by_format"`
	States      []StateCount `json:"states"`
	Quality     qualityRow   `json:"quality"`
	Last24h     activityRow  `json:"last_24h"`
	Last7d      activityRow  `json:"last_7d"`
}

func main() {
	jsonMode := flag.Bool("json", false, "output as JSON instead of human-readable text")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		fatalf("config load: %v", err)
	}
	if cfg.DatabaseURL == "" {
		fatalf("DATABASE_URL is not set")
	}

	// Load the hardcoded US states list.
	usStates, err := loadUSStates()
	if err != nil {
		fatalf("%v", err)
	}

	database, err := db.Connect(cfg.DatabaseURL, "error", cfg.ScraperDBSchema)
	if err != nil {
		fatalf("db connect: %v", err)
	}
	defer database.Close() //nolint:errcheck

	// stg is the fully-qualified staging table name, e.g. "scraper.rebates_staging".
	// It comes from SCRAPER_DB_SCHEMA (set by db.Connect via models.ScraperSchema).
	stg := models.ScraperSchema + ".rebates_staging"

	g := database.GORM()
	report := Report{GeneratedAt: time.Now().UTC().Format(time.RFC3339)}

	// ── 1. Total ──────────────────────────────────────────────────────────────
	g.Raw(`SELECT COUNT(*) FROM ` + stg + ` WHERE deleted_at IS NULL`).
		Scan(&report.Total)

	// ── 2. By promotion status ────────────────────────────────────────────────
	g.Raw(`
		SELECT stg_promotion_status AS status, COUNT(*) AS count
		FROM   ` + stg + `
		WHERE  deleted_at IS NULL
		GROUP  BY stg_promotion_status
		ORDER  BY count DESC
	`).Scan(&report.ByStatus)

	// ── 3. By source ──────────────────────────────────────────────────────────
	g.Raw(`
		SELECT
			source,
			COUNT(*)                                                  AS total,
			COUNT(*) FILTER (WHERE stg_promotion_status = 'pending')  AS pending,
			COUNT(*) FILTER (WHERE stg_promotion_status = 'promoted') AS promoted,
			COUNT(*) FILTER (WHERE stg_promotion_status = 'skipped')  AS skipped,
			ROUND(
				100.0 * COUNT(*) FILTER (WHERE stg_promotion_status = 'promoted')
				/ NULLIF(COUNT(*), 0), 1
			)                                                         AS pct_promoted
		FROM   ` + stg + `
		WHERE  deleted_at IS NULL
		GROUP  BY source
		ORDER  BY total DESC
	`).Scan(&report.BySource)

	// ── 4. By incentive format ────────────────────────────────────────────────
	g.Raw(`
		SELECT
			COALESCE(incentive_format, 'unknown') AS format,
			COUNT(*) AS count
		FROM   ` + stg + `
		WHERE  deleted_at IS NULL
		GROUP  BY incentive_format
		ORDER  BY count DESC
	`).Scan(&report.ByFormat)

	// ── 5. States — all 51 from hardcoded list, counts from DB ───────────────
	// Query counts once, build a lookup map, then merge with the static list.
	var dbStates []dbStateRow
	g.Raw(`
		SELECT state, COUNT(*) AS count
		FROM   ` + stg + `
		WHERE  deleted_at IS NULL AND state IS NOT NULL
		GROUP  BY state
	`).Scan(&dbStates)

	countByAbbr := make(map[string]int64, len(dbStates))
	for _, r := range dbStates {
		countByAbbr[strings.ToUpper(r.State)] = r.Count
	}

	report.States = make([]StateCount, len(usStates))
	for i, s := range usStates {
		report.States[i] = StateCount{
			Abbr:  s.Abbr,
			Name:  s.Name,
			Count: countByAbbr[s.Abbr], // 0 if no programs found
		}
	}
	// Sort by count desc for the terminal view (JSON keeps alphabetical order above).
	sorted := make([]StateCount, len(report.States))
	copy(sorted, report.States)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Count > sorted[j].Count })

	// ── 6. Data quality ───────────────────────────────────────────────────────
	g.Raw(`
		SELECT
			COUNT(*) FILTER (WHERE incentive_description IS NULL OR incentive_description = '') AS no_description,
			COUNT(*) FILTER (WHERE incentive_amount IS NULL AND maximum_amount IS NULL
			                   AND percent_value IS NULL AND per_unit_amount IS NULL)           AS no_amount,
			COUNT(*) FILTER (WHERE state IS NULL)                                              AS no_state,
			COUNT(*) FILTER (WHERE program_url IS NULL AND application_url IS NULL)            AS no_url,
			COUNT(*) FILTER (WHERE service_territory IS NULL)                                  AS no_service_area
		FROM ` + stg + `
		WHERE deleted_at IS NULL
	`).Scan(&report.Quality)

	// ── 7. Recent activity ────────────────────────────────────────────────────
	g.Raw(`
		SELECT
			'last_24h' AS period,
			COUNT(*) FILTER (WHERE created_at >= NOW() - INTERVAL '24 hours') AS added,
			COUNT(*) FILTER (WHERE updated_at >= NOW() - INTERVAL '24 hours'
			                   AND created_at <  NOW() - INTERVAL '24 hours') AS updated
		FROM ` + stg + `
		WHERE deleted_at IS NULL
	`).Scan(&report.Last24h)

	g.Raw(`
		SELECT
			'last_7d' AS period,
			COUNT(*) FILTER (WHERE created_at >= NOW() - INTERVAL '7 days') AS added,
			COUNT(*) FILTER (WHERE updated_at >= NOW() - INTERVAL '7 days'
			                   AND created_at <  NOW() - INTERVAL '7 days') AS updated
		FROM ` + stg + `
		WHERE deleted_at IS NULL
	`).Scan(&report.Last7d)

	// ── Output ────────────────────────────────────────────────────────────────
	if *jsonMode {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(report)
		return
	}

	printReport(report, sorted)
}

// ── Human-readable printer ────────────────────────────────────────────────────

const (
	reset  = "\033[0m"
	bold   = "\033[1m"
	cyan   = "\033[36m"
	green  = "\033[32m"
	yellow = "\033[33m"
	red    = "\033[31m"
	dim    = "\033[2m"
)

func hr() { fmt.Println(dim + strings.Repeat("─", 60) + reset) }

func header(s string) {
	fmt.Printf("\n%s%s%s\n", bold+cyan, s, reset)
	hr()
}

func printReport(r Report, statesSorted []StateCount) {
	fmt.Printf("\n%s%s%s\n", bold, "  rebates_staging — Analytics Report", reset)
	fmt.Printf("  %sGenerated:%s %s\n", dim, reset, r.GeneratedAt)
	hr()

	// ── Overview ──────────────────────────────────────────────────────────────
	header("  OVERVIEW")
	fmt.Printf("  %-24s %s%d%s\n", "Total rows:", bold, r.Total, reset)
	for _, s := range r.ByStatus {
		color := statusColor(s.Status)
		pct := pctOf(s.Count, r.Total)
		fmt.Printf("  %-24s %s%-8d%s %s(%.1f%%)%s\n",
			"  "+s.Status+":", color+bold, s.Count, reset, dim, pct, reset)
	}

	// ── By source ─────────────────────────────────────────────────────────────
	header("  BY SOURCE")
	fmt.Printf("  %-22s %7s %9s %9s %9s %8s\n",
		"Source", "Total", "Pending", "Promoted", "Skipped", "Prom%")
	hr()
	for _, s := range r.BySource {
		fmt.Printf("  %-22s %7d %9d %9d %9d %7.1f%%\n",
			s.Source, s.Total, s.Pending, s.Promoted, s.Skipped, s.PctProm)
	}

	// ── Incentive formats ──────────────────────────────────────────────────────
	header("  INCENTIVE FORMATS")
	for _, f := range r.ByFormat {
		bar := barChart(f.Count, r.Total, 20)
		fmt.Printf("  %-18s %s %s%d%s\n", f.Format, bar, dim, f.Count, reset)
	}

	// ── All states (from hardcoded list, sorted by count) ─────────────────────
	header(fmt.Sprintf("  ALL STATES  (%d states, sorted by count)", len(statesSorted)))
	maxCount := int64(0)
	if len(statesSorted) > 0 {
		maxCount = statesSorted[0].Count
	}
	for _, s := range statesSorted {
		bar := barChart(s.Count, maxCount, 24)
		covered := dim + "─" + reset
		if s.Count > 0 {
			covered = green + "✔" + reset
		}
		fmt.Printf("  %s %-4s %-22s %s %s%d%s\n",
			covered, s.Abbr, s.Name, bar, dim, s.Count, reset)
	}

	// ── Data quality ──────────────────────────────────────────────────────────
	header("  DATA QUALITY  (rows missing key fields)")
	printQuality("No description", r.Quality.NoDescription, r.Total)
	printQuality("No amount", r.Quality.NoAmount, r.Total)
	printQuality("No state", r.Quality.NoState, r.Total)
	printQuality("No URL", r.Quality.NoURL, r.Total)
	printQuality("No service area", r.Quality.NoServiceArea, r.Total)

	// ── Recent activity ────────────────────────────────────────────────────────
	header("  RECENT ACTIVITY")
	printActivity("Last 24 h", r.Last24h)
	printActivity("Last 7 d ", r.Last7d)

	fmt.Println()
	hr()
	fmt.Println()
}

func printQuality(label string, missing, total int64) {
	pct := pctOf(missing, total)
	color := green
	if pct > 20 {
		color = yellow
	}
	if pct > 50 {
		color = red
	}
	fmt.Printf("  %-22s %s%d%s %s(%.1f%%)%s\n",
		label+":", color+bold, missing, reset, dim, pct, reset)
}

func printActivity(label string, a activityRow) {
	fmt.Printf("  %-10s  added: %s%d%s   updated: %s%d%s\n",
		label,
		bold+green, a.Added, reset,
		bold+cyan, a.Updated, reset,
	)
}

func statusColor(s string) string {
	switch s {
	case "promoted":
		return green
	case "pending":
		return yellow
	case "skipped":
		return dim
	default:
		return reset
	}
}

func pctOf(n, total int64) float64 {
	if total == 0 {
		return 0
	}
	return float64(n) * 100.0 / float64(total)
}

func barChart(n, total int64, width int) string {
	if total == 0 {
		return dim + strings.Repeat("░", width) + reset
	}
	filled := int(float64(n) / float64(total) * float64(width))
	if filled > width {
		filled = width
	}
	return green + strings.Repeat("█", filled) + dim + strings.Repeat("░", width-filled) + reset
}

func fatalf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "staging-stats: "+format+"\n", args...)
	os.Exit(1)
}
