# SRP — Salt River Project (`srp`)

**Source:** [srpnet.com](https://www.srpnet.com) — Salt River Project (Arizona)

**Approach:** Sitemap crawl + Colly HTML scraping. PDF URLs are routed to the PDF extraction path.

---

## Geographic Coverage — How State & ZIP Are Determined

SRP serves the **greater Phoenix metropolitan area in Arizona**. State and ZIP are hardcoded on every record:

| Field | Value | Rationale |
|-------|-------|-----------|
| `State` | `"AZ"` | SRP is an Arizona-only utility |
| `ZipCode` | `"85001"` | Phoenix (SRP's headquarters city and core service area) |
| `ServiceTerritory` | `"SRP Service Area"` | Covers greater Phoenix metro |

The scraper does not sweep multiple ZIPs. SRP programs apply uniformly across their service territory, so one representative ZIP is sufficient.

---

## Approach

1. Fetches `https://www.srpnet.com/sitemap.xml` and applies `srpFilterCfg` — exclusions checked first, then inclusion keywords.
2. Falls back to hardcoded seed URLs if the sitemap is unavailable or returns no matches.
3. For each URL: PDF links → `ExtractPDFPages` + `ExtractIncentiveFromPDFText`; HTML links → Colly `extractPage()`.

---

## URL Filter

**Exclusions (checked first — any match rejects the URL):**
`/doing-business/`, `/trade-ally/`, `/about/`, `/careers/`, `/water-`, `/irrigation/`, `-workshop`, `-audit`, `-faq`, `/savings-tools`, `/diy-`, `/how-to-`

**Inclusions (must match at least one):**
`/rebates/`, `/rebate/`, `/incentive/`, `/energy-savings-rebates/`, `assistance`, `economy`, `discount`, `demand-response`, `heat-pump`, `solar`

---

## Seed URLs (Fallback)

```
https://www.srpnet.com/energy-savings-rebates/home
https://www.srpnet.com/energy-savings-rebates/business
https://www.srpnet.com/price-plans/home/time-of-use
https://www.srpnet.com/assistance
```

---

## ID Generation

`DeterministicID("srp", pageURL)` — stable per page URL.

---

## Fields Mapped

| `models.Incentive` field | Value / Source |
|--------------------------|----------------|
| `ID` | `DeterministicID("srp", pageURL)` |
| `Source` | `"srp"` (hardcoded) |
| `ProgramName` | `<h1>` text; fallback: `<title>` stripped of `" \| SRP"` suffix |
| `UtilityCompany` | `"Salt River Project"` (hardcoded) |
| `State` | `"AZ"` (hardcoded) |
| `ZipCode` | `"85001"` (Phoenix — representative AZ ZIP) |
| `ServiceTerritory` | `"SRP Service Area"` (hardcoded) |
| `IncentiveDescription` | `<meta name="description">` content; fallback: first `<p>` with >40 chars |
| `IncentiveFormat` | Parsed via `ParseAmount()` — `dollar_amount`, `percent`, `per_unit`, or `narrative` |
| `IncentiveAmount` | First dollar/percent amount found in page text, `<p>`, `<li>`, `<td>`, `<strong>` |
| `ApplicationURL` | First `<a href>` with "apply", "application", "enroll", or "sign up" |
| `ProgramURL` | The page URL being scraped |
| `ApplicationProcess` | `"Visit the official Salt River Project website to learn about eligibility requirements and submit your application."` |
| `ContactPhone` | First US phone number found in page text (regex) |
| `ContactEmail` | First email address found in page text (regex) |
| `CategoryTag` | Inferred from URL + title — see [shared.md](shared.md#category-inference) |
| `ContractorRequired` | `true` if licensed/approved contractor language found |
| `EnergyAuditRequired` | `true` if energy audit language found |
| `CustomerType` | `"Residential"`, `"Commercial"`, `"Residential, Commercial"`, or `""` |
| `StartDate` | Date after "effective", "starting", "as of" keywords |
| `EndDate` | Date after "expires", "through", "deadline" keywords |
| `AvailableNationwide` | `false` |
| `ProgramHash` | `ComputeProgramHash(ProgramName, "Salt River Project")` |
| `ScraperVersion` | From config |

**Fields NOT populated:** `segment`, `portfolio`, `maximum_amount`, `image_url`, `rate_tiers`

---

## Running

```bash
pnpm run:srp
```

Or directly via Go / Makefile:

```bash
SOURCE=srp RUN_ONCE=true go run ./cmd/scraper
make scrape-srp
```

---

## Rate Limiting

600 ms delay between requests, parallelism = 2.

---

## Cloudflare Bypass

SRP is behind Cloudflare. Requests from data-center IP ranges (AWS, GCP, Hetzner, etc.) return HTTP 403 regardless of User-Agent or headers — this is confirmed by `server: cloudflare` + `cf-ray` in the response headers from a direct `curl`. Two bypass options are available:

### Option A — Headless browser (recommended, no extra infrastructure)

```bash
USE_HEADLESS_BROWSER=true SOURCE=srp RUN_ONCE=true go run ./cmd/scraper
```

Or in `.env`:
```
USE_HEADLESS_BROWSER=true
```

Rod downloads Chromium automatically the first time (~150 MB, cached at `~/.cache/rod/browser/`). Subsequent runs reuse the cached binary. The headless browser uses a real Chrome TLS fingerprint and can solve Cloudflare JS challenges, so it works even from a data-center IP.

**On Docker / container environments**, the Chrome sandbox needs to be disabled (Rod does this automatically when `NoSandbox: true` is set, which is the default). You may also need to install shared libraries:
```bash
# Debian/Ubuntu
apt-get install -y ca-certificates libnss3 libatk-bridge2.0-0 libgbm1 libasound2
```

### Option B — Residential proxy (if you have one)

```bash
SCRAPER_PROXY_URL=http://user:pass@proxy.example.com:8080 SOURCE=srp RUN_ONCE=true go run ./cmd/scraper
```

This routes the plain HTTP client (sitemap + Colly visits) through the proxy's IP, which is not in any data-center block list.

## Configuration

| Env var | Default | Description |
|---------|---------|-------------|
| `USE_HEADLESS_BROWSER` | `false` | Use headless Chromium (go-rod) instead of Colly for page visits. Bypasses Cloudflare TLS/IP/JS-challenge blocks. Rod auto-downloads Chromium on first use. |
| `SCRAPER_PROXY_URL` | _(none)_ | Route sitemap fetches and Colly visits through a proxy. Format: `http://user:pass@host:port` or `socks5://host:port`. Alternative to headless browser if you have a residential proxy. |
