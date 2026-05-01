// progress.go — shared progress bar helper for all scrapers.
//
// The bar writes to stderr so it never mixes with zap's structured JSON output
// (which goes to stdout).  When stderr is not a TTY (CI, log files, piped
// output) the bar is automatically suppressed — only the final "100%" line
// remains.
package scrapers

import (
	"os"

	"github.com/schollz/progressbar/v3"
)

// NewProgressBar returns a consistently styled progress bar for the given
// scraper and total step count.
//
// description should be the scraper source name, e.g. "con_edison".
// total is the number of steps (URLs, states, ZIP requests, pages, …).
//
// Call bar.Add(1) on each step and bar.Finish() when done.
func NewProgressBar(total int, description string) *progressbar.ProgressBar {
	return progressbar.NewOptions(total,
		progressbar.OptionSetWriter(os.Stderr),
		progressbar.OptionSetDescription(padDescription(description)),
		progressbar.OptionSetWidth(35),
		progressbar.OptionShowCount(),
		progressbar.OptionShowIts(),
		progressbar.OptionSetItsString("req"),
		progressbar.OptionSetTheme(progressbar.Theme{
			Saucer:        "█",
			SaucerPadding: "░",
			BarStart:      " |",
			BarEnd:        "|",
		}),
		progressbar.OptionEnableColorCodes(true),
		progressbar.OptionSetPredictTime(true),
		progressbar.OptionFullWidth(),
		progressbar.OptionOnCompletion(func() {
			// newline after bar completes so next log line starts cleanly
			os.Stderr.WriteString("\n")
		}),
	)
}

// padDescription right-pads the description to a fixed width so bars from
// different scrapers line up visually when running sequentially.
func padDescription(s string) string {
	const width = 28
	for len(s) < width {
		s += " "
	}
	if len(s) > width {
		s = s[:width]
	}
	return "[cyan]" + s + "[reset]"
}
