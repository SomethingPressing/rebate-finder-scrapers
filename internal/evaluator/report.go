package evaluator

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

// PrintReport writes results to stdout in either "table" or "json" format.
func PrintReport(results []EvalResult, format string) {
	if format == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(results)
		return
	}

	fmt.Printf("\n╔══════════════════════════════════════════════════════════════════╗\n")
	fmt.Printf("║          SCRAPER DATA QUALITY EVALUATION REPORT                  ║\n")
	fmt.Printf("╚══════════════════════════════════════════════════════════════════╝\n\n")

	// Group by source for a cleaner read
	bySource := make(map[string][]EvalResult)
	var sources []string
	for _, r := range results {
		if _, ok := bySource[r.Source]; !ok {
			sources = append(sources, r.Source)
		}
		bySource[r.Source] = append(bySource[r.Source], r)
	}
	sort.Strings(sources)

	var totalScore float64
	var totalCount int

	for _, src := range sources {
		rows := bySource[src]
		var srcScore float64
		for _, r := range rows {
			srcScore += r.OverallScore
		}
		avg := srcScore / float64(len(rows))
		totalScore += srcScore
		totalCount += len(rows)

		fmt.Printf("┌─ SOURCE: %-20s  avg %.0f%%  (%d programs)\n", src, avg*100, len(rows))
		for _, r := range rows {
			printResult(r)
		}
	}

	if totalCount > 0 {
		overall := totalScore / float64(totalCount)
		fmt.Printf("╔══════════════════════════════════════════════════════════════════╗\n")
		fmt.Printf("║  OVERALL AVERAGE SCORE: %3.0f%%  across %d programs               \n", overall*100, totalCount)
		fmt.Printf("╚══════════════════════════════════════════════════════════════════╝\n\n")

		printGapSummary(results)
	}
}

// dbFieldOrder controls the display order and which fields appear in the DB section.
var dbFieldOrder = []string{
	"program_name", "utility_company", "source", "scraper_version", "promotion_status", "rebate_id",
	"incentive_format", "incentive_amount", "maximum_amount", "percent_value", "per_unit_amount", "unit_type",
	"incentive_description",
	"state", "zip_code", "service_territory", "available_nationwide",
	"categories", "portfolio", "implementing_sector", "segment", "customer_type", "administrator",
	"start_date", "end_date", "while_funds_last",
	"source_url", "program_url", "application_url", "application_process",
	"contact_email", "contact_phone",
	"contractor_required", "energy_audit_required",
	"source_id",
}

func printResult(r EvalResult) {
	scoreBar := scoreToBar(r.OverallScore)
	fmt.Printf("│\n")
	fmt.Printf("│  Program : %s\n", r.ProgramName)
	if r.SourceURL != "" {
		fmt.Printf("│  Source  : %s\n", r.SourceURL)
	}
	if r.ProgramURL != "" && r.ProgramURL != r.SourceURL {
		fmt.Printf("│  URL     : %s\n", r.ProgramURL)
	}
	fmt.Printf("│  Score   : %s  %.0f%%\n", scoreBar, r.OverallScore*100)

	if r.Error != "" {
		fmt.Printf("│  ⚠ Error : %s\n", r.Error)
		fmt.Printf("│\n")
		return
	}

	// ── Staging DB values ──────────────────────────────────────────────────
	if len(r.DBValues) > 0 {
		fmt.Printf("│\n│  ┌─ STAGED DB VALUES ─────────────────────────────────────────\n")
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		// Print fields in the canonical order; skip any not present in this row.
		printed := make(map[string]bool)
		for _, key := range dbFieldOrder {
			if val, ok := r.DBValues[key]; ok {
				displayed := val
				if key == "incentive_description" || key == "application_process" {
					if len(displayed) > 120 {
						displayed = displayed[:117] + "..."
					}
				}
				fmt.Fprintf(w, "│  │  %-25s\t%s\n", key, displayed)
				printed[key] = true
			}
		}
		// Any extra fields not in the canonical order (future-proofing).
		extraKeys := make([]string, 0)
		for k := range r.DBValues {
			if !printed[k] {
				extraKeys = append(extraKeys, k)
			}
		}
		sort.Strings(extraKeys)
		for _, key := range extraKeys {
			fmt.Fprintf(w, "│  │  %-25s\t%s\n", key, r.DBValues[key])
		}
		_ = w.Flush()
		fmt.Printf("│  └────────────────────────────────────────────────────────────\n")
	}

	// ── Field-by-field comparison ──────────────────────────────────────────
	fmt.Printf("│\n")
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "│  %-22s\t%-15s\t%-30s\t%-30s\n", "FIELD", "STATUS", "SCRAPER (fresh)", "LLM")
	fmt.Fprintf(w, "│  %-22s\t%-15s\t%-30s\t%-30s\n",
		"──────────────────────", "───────────────", "────────────────────────────────", "────────────────────────────────")
	for _, fs := range r.FieldScores {
		fmt.Fprintf(w, "│  %-22s\t%-15s\t%-30s\t%-30s\n",
			fs.Name, string(fs.Status), fs.ScraperValue, fs.LLMValue)
	}
	_ = w.Flush()

	if len(r.MissingFields) > 0 {
		fmt.Printf("│\n│  GAPS (scraper missed or mismatched): %v\n", r.MissingFields)
	}
}

// ensure strings is used (sort is already used elsewhere)
var _ = strings.Join

// PrintReportMarkdown writes the full evaluation report to a Markdown file at
// the given path and prints "Report written to <path>" on stderr.
func PrintReportMarkdown(results []EvalResult, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := f

	// ── Header ────────────────────────────────────────────────────────────────
	fmt.Fprintf(w, "# Scraper Data Quality Evaluation\n\n")
	fmt.Fprintf(w, "**Generated:** %s\n\n", time.Now().Format("2006-01-02 15:04:05"))

	// Compute overall stats.
	var totalScore float64
	var totalCount int
	for _, r := range results {
		if r.Error == "" {
			totalScore += r.OverallScore
			totalCount++
		}
	}
	if totalCount > 0 {
		fmt.Fprintf(w, "**Programs evaluated:** %d  \n", totalCount)
		fmt.Fprintf(w, "**Overall average score:** %.0f%%\n\n", (totalScore/float64(totalCount))*100)
	}
	fmt.Fprintf(w, "---\n\n")

	// ── Per-source sections ───────────────────────────────────────────────────
	bySource := make(map[string][]EvalResult)
	var sources []string
	for _, r := range results {
		if _, ok := bySource[r.Source]; !ok {
			sources = append(sources, r.Source)
		}
		bySource[r.Source] = append(bySource[r.Source], r)
	}
	sort.Strings(sources)

	for _, src := range sources {
		rows := bySource[src]
		var srcScore float64
		var srcCount int
		for _, r := range rows {
			if r.Error == "" {
				srcScore += r.OverallScore
				srcCount++
			}
		}
		avg := 0.0
		if srcCount > 0 {
			avg = (srcScore / float64(srcCount)) * 100
		}
		fmt.Fprintf(w, "## %s — avg %.0f%% (%d programs)\n\n", src, avg, len(rows))

		for _, r := range rows {
			mdPrintResult(w, r)
		}
	}

	// ── Gap summary ───────────────────────────────────────────────────────────
	freq := make(map[string]int)
	for _, r := range results {
		for _, f := range r.MissingFields {
			freq[f]++
		}
	}
	if len(freq) > 0 {
		fmt.Fprintf(w, "---\n\n## Top Gaps Across All Programs\n\n")
		fmt.Fprintf(w, "| Field | Programs Affected |\n")
		fmt.Fprintf(w, "|-------|------------------|\n")
		type kv struct{ k string; v int }
		var sorted []kv
		for k, v := range freq {
			sorted = append(sorted, kv{k, v})
		}
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].v > sorted[j].v })
		for _, kv := range sorted {
			fmt.Fprintf(w, "| `%s` | %d |\n", kv.k, kv.v)
		}
		fmt.Fprintf(w, "\n")
	}

	fmt.Fprintf(os.Stderr, "Report written to %s\n", path)
	return nil
}

// mdPrintResult writes one program's result block to the markdown file.
func mdPrintResult(w *os.File, r EvalResult) {
	score := r.OverallScore * 100
	emoji := scoreEmoji(r.OverallScore)

	fmt.Fprintf(w, "---\n\n")
	fmt.Fprintf(w, "### %s\n\n", mdEscape(r.ProgramName))
	fmt.Fprintf(w, "**Score:** %.0f%% %s\n\n", score, emoji)
	if r.SourceURL != "" {
		fmt.Fprintf(w, "**Source:** [%s](%s)", r.SourceURL, r.SourceURL)
		if r.ProgramURL != "" && r.ProgramURL != r.SourceURL {
			fmt.Fprintf(w, "  \n**Program URL:** [%s](%s)", r.ProgramURL, r.ProgramURL)
		}
		fmt.Fprintf(w, "\n\n")
	} else if r.ProgramURL != "" {
		fmt.Fprintf(w, "**Program URL:** [%s](%s)\n\n", r.ProgramURL, r.ProgramURL)
	}

	if r.Error != "" {
		fmt.Fprintf(w, "> ⚠️ **Error:** %s\n\n", r.Error)
		return
	}

	// ── Staged DB values ──────────────────────────────────────────────────
	if len(r.DBValues) > 0 {
		fmt.Fprintf(w, "#### Staged DB Values\n\n")
		fmt.Fprintf(w, "| Field | Value |\n")
		fmt.Fprintf(w, "|-------|-------|\n")
		printed := make(map[string]bool)
		for _, key := range dbFieldOrder {
			if val, ok := r.DBValues[key]; ok {
				displayed := val
				if key == "incentive_description" || key == "application_process" {
					displayed = strings.ReplaceAll(displayed, "\n", " ")
				}
				fmt.Fprintf(w, "| `%s` | %s |\n", key, mdEscape(displayed))
				printed[key] = true
			}
		}
		// Extra fields not in canonical order.
		var extras []string
		for k := range r.DBValues {
			if !printed[k] {
				extras = append(extras, k)
			}
		}
		sort.Strings(extras)
		for _, key := range extras {
			fmt.Fprintf(w, "| `%s` | %s |\n", key, mdEscape(r.DBValues[key]))
		}
		fmt.Fprintf(w, "\n")
	}

	// ── Field comparison table ────────────────────────────────────────────
	fmt.Fprintf(w, "#### Field Comparison\n\n")
	fmt.Fprintf(w, "| Field | Status | Scraper (fresh) | LLM |\n")
	fmt.Fprintf(w, "|-------|--------|----------------|-----|\n")
	for _, fs := range r.FieldScores {
		if fs.Status == StatusEmptyBoth {
			continue // skip rows where both sides are empty — not interesting
		}
		fmt.Fprintf(w, "| `%s` | %s | %s | %s |\n",
			fs.Name,
			mdStatusBadge(fs.Status),
			mdEscape(fs.ScraperValue),
			mdEscape(fs.LLMValue),
		)
	}
	fmt.Fprintf(w, "\n")

	if len(r.MissingFields) > 0 {
		fmt.Fprintf(w, "**Gaps:** ")
		for i, f := range r.MissingFields {
			if i > 0 {
				fmt.Fprintf(w, ", ")
			}
			fmt.Fprintf(w, "`%s`", f)
		}
		fmt.Fprintf(w, "\n\n")
	}
}

func scoreEmoji(score float64) string {
	switch {
	case score >= 0.85:
		return "🟢"
	case score >= 0.65:
		return "🟡"
	case score >= 0.40:
		return "🟠"
	default:
		return "🔴"
	}
}

func mdStatusBadge(s FieldStatus) string {
	switch s {
	case StatusMatch:
		return "✅ match"
	case StatusPartial:
		return "🔶 partial"
	case StatusMissing:
		return "❌ missing"
	case StatusMismatch:
		return "⚠️ mismatch"
	case StatusScraperOnly:
		return "🔵 scraper-only"
	default:
		return string(s)
	}
}

// mdEscape escapes pipe characters so they don't break markdown tables.
func mdEscape(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

// printGapSummary prints a cross-program frequency table of missing fields.
func printGapSummary(results []EvalResult) {
	freq := make(map[string]int)
	for _, r := range results {
		for _, f := range r.MissingFields {
			freq[f]++
		}
	}
	if len(freq) == 0 {
		return
	}

	type kv struct {
		field string
		count int
	}
	var sorted []kv
	for k, v := range freq {
		sorted = append(sorted, kv{k, v})
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].count > sorted[j].count })

	fmt.Printf("TOP GAPS (fields the scraper missed that LLM found):\n")
	fmt.Printf("  %-25s  %s\n", "FIELD", "PROGRAMS MISSING")
	fmt.Printf("  %-25s  %s\n", "─────────────────────────", "────────────────")
	for _, kv := range sorted {
		bar := ""
		for i := 0; i < kv.count; i++ {
			bar += "█"
		}
		fmt.Printf("  %-25s  %d  %s\n", kv.field, kv.count, bar)
	}
	fmt.Println()
}

func scoreToBar(score float64) string {
	filled := int(score * 10)
	bar := "["
	for i := 0; i < 10; i++ {
		if i < filled {
			bar += "█"
		} else {
			bar += "░"
		}
	}
	bar += "]"
	return bar
}
