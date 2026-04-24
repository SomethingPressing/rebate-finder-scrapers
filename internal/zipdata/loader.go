// Package zipdata loads US ZIP code data from a uszips.csv file and provides
// helpers for sampling ZIPs per state — used by scrapers that require one or
// more representative ZIPs per state to discover available incentive programs.
package zipdata

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// StateZIPs maps a 2-letter state abbreviation → ordered list of ZIP codes.
// ZIPs are sorted by descending population so that Sample(n=1) returns the
// most populous ZIP in each state (best proxy for a state's utility mix).
type StateZIPs map[string][]string

// usStates is the set of 50 US states + DC. Territories (PR, VI, GU, AS, MP)
// are excluded from DSIRE and Energy Star queries.
var usStates = map[string]bool{
	"AL": true, "AK": true, "AZ": true, "AR": true, "CA": true,
	"CO": true, "CT": true, "DE": true, "DC": true, "FL": true,
	"GA": true, "HI": true, "ID": true, "IL": true, "IN": true,
	"IA": true, "KS": true, "KY": true, "LA": true, "ME": true,
	"MD": true, "MA": true, "MI": true, "MN": true, "MS": true,
	"MO": true, "MT": true, "NE": true, "NV": true, "NH": true,
	"NJ": true, "NM": true, "NY": true, "NC": true, "ND": true,
	"OH": true, "OK": true, "OR": true, "PA": true, "RI": true,
	"SC": true, "SD": true, "TN": true, "TX": true, "UT": true,
	"VT": true, "VA": true, "WA": true, "WV": true, "WI": true,
	"WY": true,
}

// zipRow holds the parsed fields we care about from uszips.csv.
type zipRow struct {
	zip        string
	stateID    string
	population int
	zcta       bool // Zip Code Tabulation Area — real deliverable ZIP
	military   bool
}

// Load reads a uszips.csv file at the given path and returns ZIPs grouped by
// state, sorted by descending population.
//
// Filtering applied:
//   - Only US states + DC (no territories: PR, VI, GU, AS, MP).
//   - Only ZCTA rows (real deliverable ZIPs; zcta == "TRUE").
//   - Military ZIPs (APO/FPO/DPO) are excluded.
func Load(csvPath string) (StateZIPs, error) {
	f, err := os.Open(csvPath)
	if err != nil {
		return nil, fmt.Errorf("zipdata: open %s: %w", csvPath, err)
	}
	defer f.Close()
	return parse(f)
}

// AutoLoad searches for uszips.csv in standard locations relative to the
// process working directory and the source file location (useful in tests).
// Returns ErrNotFound if the file cannot be found.
func AutoLoad() (StateZIPs, error) {
	candidates := searchPaths()
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return Load(c)
		}
	}
	return nil, fmt.Errorf("zipdata: uszips.csv not found in any of: %v", candidates)
}

// LoadPath loads ZIPs from csvPath if non-empty, otherwise falls back to AutoLoad.
func LoadPath(csvPath string) (StateZIPs, error) {
	if csvPath != "" {
		return Load(csvPath)
	}
	return AutoLoad()
}

// Sample returns up to n ZIPs per state, flattened into a single slice.
// States are iterated in alphabetical order for deterministic output.
// ZIPs within each state are already sorted by descending population, so
// Sample(n=1) returns the most populous ZIP per state.
//
// n=0 means no limit — all ZIPs for every state are returned.
func Sample(stateZIPs StateZIPs, n int) []string {
	states := make([]string, 0, len(stateZIPs))
	for s := range stateZIPs {
		states = append(states, s)
	}
	sort.Strings(states)

	var out []string
	for _, state := range states {
		zips := stateZIPs[state]
		if n == 0 {
			// n=0 → no limit, take all ZIPs for this state.
			out = append(out, zips...)
		} else {
			take := n
			if take > len(zips) {
				take = len(zips)
			}
			out = append(out, zips[:take]...)
		}
	}
	return out
}

// ── internal ──────────────────────────────────────────────────────────────────

func parse(r io.Reader) (StateZIPs, error) {
	cr := csv.NewReader(r)
	cr.LazyQuotes = true
	cr.TrimLeadingSpace = true

	// Read header to find column indices.
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("zipdata: read header: %w", err)
	}
	idx := indexColumns(header)
	for _, col := range []string{"zip", "state_id", "zcta", "military", "population"} {
		if _, ok := idx[col]; !ok {
			return nil, fmt.Errorf("zipdata: column %q not found in CSV header", col)
		}
	}

	// Accumulate by state, grouped for sorting.
	type stateEntry struct {
		zip string
		pop int
	}
	stateMap := make(map[string][]stateEntry)

	for {
		row, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Skip malformed rows.
			continue
		}

		zr := zipRow{
			zip:      strings.TrimSpace(row[idx["zip"]]),
			stateID:  strings.ToUpper(strings.TrimSpace(row[idx["state_id"]])),
			zcta:     strings.EqualFold(strings.TrimSpace(row[idx["zcta"]]), "true"),
			military: strings.EqualFold(strings.TrimSpace(row[idx["military"]]), "true"),
		}
		if p, err := strconv.Atoi(strings.TrimSpace(row[idx["population"]])); err == nil {
			zr.population = p
		}

		// Filter
		if !usStates[zr.stateID] {
			continue
		}
		if !zr.zcta || zr.military {
			continue
		}
		if zr.zip == "" {
			continue
		}

		stateMap[zr.stateID] = append(stateMap[zr.stateID], stateEntry{zip: zr.zip, pop: zr.population})
	}

	// Sort each state's ZIPs by descending population.
	result := make(StateZIPs, len(stateMap))
	for state, entries := range stateMap {
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].pop > entries[j].pop
		})
		zips := make([]string, len(entries))
		for i, e := range entries {
			zips[i] = e.zip
		}
		result[state] = zips
	}

	return result, nil
}

// indexColumns returns a map of column name → index for the CSV header row.
// Column names are normalised to lowercase and surrounding quotes stripped.
func indexColumns(header []string) map[string]int {
	m := make(map[string]int, len(header))
	for i, h := range header {
		key := strings.ToLower(strings.Trim(strings.TrimSpace(h), `"`))
		m[key] = i
	}
	return m
}

// searchPaths returns candidate paths for uszips.csv, relative to cwd and
// the caller's source file directory (for test execution from sub-packages).
func searchPaths() []string {
	candidates := []string{
		"data/uszips.csv",
		"../data/uszips.csv",
		"../../data/uszips.csv",
		"../../../data/uszips.csv",
	}

	// Also try relative to this source file (works when running go test ./...).
	_, thisFile, _, ok := runtime.Caller(0)
	if ok {
		dir := filepath.Dir(thisFile) // internal/zipdata/
		for i := 0; i < 4; i++ {
			candidate := filepath.Join(dir, "data/uszips.csv")
			candidates = append(candidates, candidate)
			dir = filepath.Dir(dir)
		}
	}

	return candidates
}
