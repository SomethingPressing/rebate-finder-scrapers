package evaluator

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"
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

func printResult(r EvalResult) {
	scoreBar := scoreToBar(r.OverallScore)
	fmt.Printf("│\n")
	fmt.Printf("│  Program : %s\n", r.ProgramName)
	if r.ProgramURL != "" {
		fmt.Printf("│  URL     : %s\n", r.ProgramURL)
	}
	fmt.Printf("│  Score   : %s  %.0f%%\n", scoreBar, r.OverallScore*100)

	if r.Error != "" {
		fmt.Printf("│  ⚠ Error : %s\n", r.Error)
		fmt.Printf("│\n")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "│  %-22s\t%-15s\t%-30s\t%-30s\n", "FIELD", "STATUS", "SCRAPER", "LLM")
	fmt.Fprintf(w, "│  %-22s\t%-15s\t%-30s\t%-30s\n",
		"──────────────────────", "───────────────", "────────────────────────────", "────────────────────────────")
	for _, fs := range r.FieldScores {
		fmt.Fprintf(w, "│  %-22s\t%-15s\t%-30s\t%-30s\n",
			fs.Name, string(fs.Status), fs.ScraperValue, fs.LLMValue)
	}
	_ = w.Flush()

	if len(r.MissingFields) > 0 {
		fmt.Printf("│\n│  GAPS (scraper missed or mismatched): %v\n", r.MissingFields)
	}
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
