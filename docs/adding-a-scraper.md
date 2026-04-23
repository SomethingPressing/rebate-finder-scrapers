# Adding a New Scraper

This guide walks through implementing and registering a new scraper from scratch.

---

## 1. Create the scraper file

Create `scrapers/your_source.go`:

```go
package scrapers

import (
    "context"
    "fmt"

    "github.com/incenva/rebate-scraper/models"
    "go.uber.org/zap"
)

const yourSourceName = "your_source"   // stable identifier — written to the source column

// YourSourceScraper fetches incentives from Your Source.
type YourSourceScraper struct {
    baseURL string
    log     *zap.Logger
}

// NewYourSourceScraper creates a scraper with the given base URL and logger.
func NewYourSourceScraper(baseURL string, log *zap.Logger) *YourSourceScraper {
    return &YourSourceScraper{
        baseURL: baseURL,
        log:     log,
    }
}

// Name returns the stable identifier for this scraper.
func (s *YourSourceScraper) Name() string { return yourSourceName }

// Scrape fetches all incentives from Your Source.
func (s *YourSourceScraper) Scrape(ctx context.Context) ([]models.Incentive, error) {
    var results []models.Incentive

    // TODO: fetch + parse data from your source
    // Example: REST API call
    resp, err := fetchJSON(ctx, s.baseURL+"/api/incentives")
    if err != nil {
        return nil, fmt.Errorf("%s: fetch: %w", yourSourceName, err)
    }

    for _, item := range resp.Items {
        inc := models.NewIncentive(yourSourceName, "1.0")

        // Use DeterministicID so re-scraping the same program produces the same UUID
        inc.ID = models.DeterministicID(yourSourceName, item.ExternalID)

        inc.ProgramName    = item.Name
        inc.UtilityCompany = item.Utility
        inc.State          = models.PtrString(item.State)
        // ... set other fields

        // Compute program hash (required for deduplication in the promoter)
        inc.ProgramHash = models.ComputeProgramHash(inc.ProgramName, inc.UtilityCompany)

        results = append(results, inc)
    }

    s.log.Info("scrape complete",
        zap.String("source", yourSourceName),
        zap.Int("count", len(results)),
    )
    return results, nil
}
```

---

## 2. Add configuration (if needed)

If the scraper needs a base URL or API key, add it to `config/config.go`:

```go
type Config struct {
    // ... existing fields ...
    YourSourceBaseURL string
    YourSourceAPIKey  string
}

func Load() (*Config, error) {
    // ... existing code ...
    return &Config{
        // ... existing fields ...
        YourSourceBaseURL: getEnv("YOUR_SOURCE_BASE_URL", "https://api.yoursource.com"),
        YourSourceAPIKey:  getEnv("YOUR_SOURCE_API_KEY", ""),
    }, nil
}
```

Add the variable to `.env.example`:

```env
YOUR_SOURCE_BASE_URL=https://api.yoursource.com   # optional — has default
YOUR_SOURCE_API_KEY=                              # required if using authenticated API
```

---

## 3. Register the scraper in `cmd/scraper/main.go`

```go
import (
    // ...
    "github.com/incenva/rebate-scraper/scrapers"
)

func main() {
    // ... existing setup ...

    reg := scrapers.NewRegistry()
    reg.Register(scrapers.NewDSIREUScraper(cfg.DSIREBaseURL, log))
    reg.Register(scrapers.NewRewiringAmericaScraper(cfg.RewiringAmericaBaseURL, cfg.RewiringAmericaAPIKey, log))
    reg.Register(scrapers.NewEnergyStarScraper(cfg.EnergyStarBaseURL, log))

    // Add your new scraper:
    reg.Register(scrapers.NewYourSourceScraper(cfg.YourSourceBaseURL, log))

    // ... rest of main ...
}
```

---

## 4. Test it

```bash
# Run only your new scraper
SOURCE=your_source RUN_ONCE=true LOG_FORMAT=console go run ./cmd/scraper

# Verify rows landed in staging
psql $DATABASE_URL -c "
SELECT program_name, utility_company, state, incentive_format, incentive_amount
FROM rebates_staging
WHERE source = 'your_source'
ORDER BY created_at DESC
LIMIT 10;
"
```

---

## Deterministic IDs

Always use `models.DeterministicID` when the external source has its own stable identifier (integer ID, slug, etc.). This ensures re-scraping never creates duplicate rows:

```go
// DSIRE uses integer program IDs
inc.ID = models.DeterministicID("dsireusa", strconv.Itoa(program.ID))

// Rewiring America uses program name + technology
inc.ID = models.DeterministicID("rewiring_america", programName+"|"+technology)

// PDF scraper uses the measure key
inc.ID = models.DeterministicID("consumers_energy_pdf", "hvac_air_conditioning")
```

If the source has no stable ID, generate a UUID from content that uniquely identifies the program:

```go
inc.ID = models.DeterministicID("your_source", program.Name+"|"+program.UtilityName)
```

---

## Program Hash

Always set `ProgramHash` — the promoter uses it as the deduplication key when merging staging rows into live rebates:

```go
inc.ProgramHash = models.ComputeProgramHash(inc.ProgramName, inc.UtilityCompany)
```

`ComputeProgramHash` = SHA-256 of `normalize(name)|normalize(utility_company)`. Source is excluded so the same program scraped by multiple sources merges into one rebate.

---

## Using Colly for HTML Scrapers

Embed `CollyBase` to get a pre-configured Colly collector with rate limiting:

```go
type YourHTMLScraper struct {
    CollyBase
}

func (s *YourHTMLScraper) Scrape(ctx context.Context) ([]models.Incentive, error) {
    c := s.NewCollector()
    var results []models.Incentive

    c.OnHTML(".incentive-card", func(e *colly.HTMLElement) {
        inc := models.NewIncentive("your_source", "1.0")
        inc.ProgramName    = strings.TrimSpace(e.ChildText(".title"))
        inc.UtilityCompany = strings.TrimSpace(e.ChildText(".utility"))

        // Parse dollar/percent amounts
        format, amount := ParseAmount(e.ChildText(".amount"))
        inc.IncentiveFormat = &format
        inc.IncentiveAmount = amount

        inc.ID          = models.DeterministicID("your_source", inc.ProgramName)
        inc.ProgramHash = models.ComputeProgramHash(inc.ProgramName, inc.UtilityCompany)

        results = append(results, inc)
    })

    if err := c.Visit(s.baseURL + "/incentives"); err != nil {
        return nil, err
    }
    return results, nil
}
```

---

## Checklist

- [ ] Scraper file created in `scrapers/`
- [ ] Implements `scrapers.Scraper` interface (`Name()` + `Scrape()`)
- [ ] Uses `models.DeterministicID` for stable `stg_source_id`
- [ ] Sets `models.ComputeProgramHash` on every incentive
- [ ] Registered in `cmd/scraper/main.go`
- [ ] Config added to `config/config.go` if needed
- [ ] Env var added to `.env.example` if needed
- [ ] Tested with `SOURCE=your_source RUN_ONCE=true go run ./cmd/scraper`
- [ ] Verified rows appear in `rebates_staging` with correct data
