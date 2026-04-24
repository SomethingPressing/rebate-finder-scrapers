package main

import (
	"fmt"
	"github.com/incenva/rebate-scraper/internal/zipdata"
)

func main() {
	zips, err := zipdata.Load("data/uszips.csv")
	if err != nil {
		panic(err)
	}
	fmt.Printf("States loaded: %d\n", len(zips))
	all := zipdata.Sample(zips, 9999)
	fmt.Printf("All ZIPs (no limit) → %d ZIPs\n", len(all))
	sample1 := zipdata.Sample(zips, 1)
	fmt.Printf("Sample(1) → %d ZIPs\n", len(sample1))
	for _, state := range []string{"CA", "TX", "NY", "FL"} {
		if z := zips[state]; len(z) > 0 {
			fmt.Printf("  %s: %d ZIPs, top: %s\n", state, len(z), z[0])
		}
	}
}
