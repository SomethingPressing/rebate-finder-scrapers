// Command purge-no-url removes staging rows that have neither a program_url
// nor an application_url — rows that can never be surfaced to users.
//
// Usage:
//
//	./purge-no-url                # list affected rows and delete them
//	./purge-no-url --dry-run      # list only, no deletions
//	./purge-no-url --source pnm   # restrict to one source
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/incenva/rebate-scraper/config"
	"github.com/incenva/rebate-scraper/db"
	"github.com/incenva/rebate-scraper/models"
)

func main() {
	dryRun := flag.Bool("dry-run", false, "preview rows to delete without making any changes")
	source := flag.String("source", "", "restrict to this scraper source (e.g. pnm, dsireusa)")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		fatalf("config: %v", err)
	}
	if cfg.DatabaseURL == "" {
		fatalf("DATABASE_URL is not set")
	}

	database, err := db.Connect(cfg.DatabaseURL, "error", cfg.ScraperDBSchema)
	if err != nil {
		fatalf("db connect: %v", err)
	}
	defer database.Close() //nolint:errcheck

	stg := models.ScraperSchema + ".rebates_staging"
	g := database.GORM()

	// ── Count by source ───────────────────────────────────────────────────────
	type sourceCount struct {
		Source string `gorm:"column:source"`
		Count  int64  `gorm:"column:count"`
	}
	baseWhere := stg + ` WHERE deleted_at IS NULL AND program_url IS NULL AND application_url IS NULL`
	if *source != "" {
		baseWhere += ` AND source = '` + strings.ReplaceAll(*source, "'", "''") + `'`
	}

	var rows []sourceCount
	g.Raw(`SELECT source, COUNT(*) AS count FROM ` + baseWhere + ` GROUP BY source ORDER BY count DESC`).Scan(&rows)

	if len(rows) == 0 {
		fmt.Println("No staging rows without URLs found.")
		return
	}

	// ── Print preview ─────────────────────────────────────────────────────────
	w := tabwriter.NewWriter(os.Stdout, 2, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  Source\tRows")
	fmt.Fprintln(w, "  "+strings.Repeat("─", 26)+"\t"+strings.Repeat("─", 6))
	var total int64
	for _, r := range rows {
		fmt.Fprintf(w, "  %s\t%d\n", r.Source, r.Count)
		total += r.Count
	}
	fmt.Fprintf(w, "  %s\t%s\n", strings.Repeat("─", 26), strings.Repeat("─", 6))
	fmt.Fprintf(w, "  Total\t%d\n", total)
	w.Flush()
	fmt.Println()

	if *dryRun {
		fmt.Println("  --dry-run: no rows deleted.")
		return
	}

	// ── Delete ────────────────────────────────────────────────────────────────
	deleteSQL := `DELETE FROM ` + baseWhere
	result := g.Exec(deleteSQL)
	if result.Error != nil {
		fatalf("delete: %v", result.Error)
	}
	fmt.Printf("  Deleted %d rows with no URL.\n", result.RowsAffected)
}

func fatalf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "purge-no-url: "+format+"\n", args...)
	os.Exit(1)
}
