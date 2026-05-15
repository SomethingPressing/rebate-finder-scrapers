// cmd/evaluator — scraper data quality evaluator.
//
// Two modes:
//
//	db         (default) — sample recent rows from the staging table, send each
//	                        to GPT-4o, diff LLM output vs what the scraper stored.
//
//	testcases  — fetch every URL in testdata/eval_testcases.json (curated known-
//	              good program pages per scraper), send to GPT-4o, diff against
//	              the matching staging row (or report "not yet scraped").
//
// Usage:
//
//	go run ./cmd/evaluator                            # db mode, 2 rows/source
//	go run ./cmd/evaluator -n 5                       # db mode, 5 rows/source
//	go run ./cmd/evaluator -source con_edison -n 3    # one scraper only
//	go run ./cmd/evaluator -mode testcases            # testcases mode, all scrapers
//	go run ./cmd/evaluator -mode testcases -source srp
//	go run ./cmd/evaluator -output json               # machine-readable JSON
//
// Output:
//
//	Always writes a Markdown report to tmp/eval_report.md (git-ignored).
//	-output table  also prints a summary table to stdout (default)
//	-output json   also prints JSON to stdout
//
// Requirements:
//
//	DATABASE_URL   — same Postgres DSN used by the scraper
//	OPENAI_API_KEY — GPT-4o API key
//
// Both can be set in the project .env file (loaded automatically).
package main

import (
	"flag"
	"log"
	"os"

	"github.com/incenva/rebate-scraper/db"
	"github.com/incenva/rebate-scraper/internal/evaluator"
	"github.com/joho/godotenv"
)

const reportDir  = "tmp"
const reportFile = "tmp/eval_report.md"

func main() {
	mode   := flag.String("mode",   "db",    "evaluation mode: db or testcases")
	source := flag.String("source", "",      "filter by scraper source (e.g. con_edison, dsireusa)")
	n      := flag.Int("n",         2,       "rows to sample per source (db mode only)")
	output := flag.String("output", "table", "output format: table or json")
	debug  := flag.Bool("debug",    false,   "print raw content sent to LLM and full LLM response to stderr")
	flag.Parse()

	_ = godotenv.Load()

	openaiKey := os.Getenv("OPENAI_API_KEY")
	if openaiKey == "" {
		log.Fatal("OPENAI_API_KEY is required (set in .env or environment)")
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL is required")
	}
	scraperSchema := os.Getenv("SCRAPER_DB_SCHEMA")
	if scraperSchema == "" {
		scraperSchema = "scraper"
	}

	gormDB, err := db.Connect(dbURL, "silent", scraperSchema)
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer gormDB.Close()

	cfg := evaluator.Config{
		DB:           gormDB,
		OpenAIKey:    openaiKey,
		Source:       *source,
		SampleN:      *n,
		OutputFormat: *output,
		Debug:        *debug,
	}

	var results []evaluator.EvalResult

	switch *mode {
	case "testcases":
		log.Printf("mode=testcases  source=%q", *source)
		results, err = evaluator.RunTestcases(cfg, *source)
	case "db":
		log.Printf("mode=db  n=%d  source=%q", *n, *source)
		results, err = evaluator.Run(cfg)
	default:
		log.Fatalf("unknown mode %q — use 'db' or 'testcases'", *mode)
	}

	if err != nil {
		log.Fatalf("evaluation failed: %v", err)
	}

	// Always write the full Markdown report to tmp/eval_report.md.
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		log.Printf("warning: could not create %s: %v", reportDir, err)
	} else if err := evaluator.PrintReportMarkdown(results, reportFile); err != nil {
		log.Printf("warning: could not write markdown report: %v", err)
	}

	// Also print to stdout in the requested format.
	evaluator.PrintReport(results, *output)
}
