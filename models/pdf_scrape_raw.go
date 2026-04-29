package models

import (
	"time"

	"gorm.io/gorm"
)

// PDFScrapeRaw stores the raw text extracted from a PDF page range for one
// measure.  Rows are upserted on (source, measure_key, pdf_type) so that
// re-running the scraper refreshes the text rather than appending duplicates.
//
// This table is an audit trail — it lets you compare what the PDF says
// against what ended up in rebates_staging / rebates.
type PDFScrapeRaw struct {
	gorm.Model // id (uint PK), created_at, updated_at, deleted_at

	// Source identifies the scraper that produced this row (e.g. "consumers_energy_pdf").
	Source string `gorm:"column:source;not null;uniqueIndex:idx_pdf_raw_unique"`

	// MeasureKey is the machine-readable measure identifier
	// (e.g. "hvac_air_conditioning", "interior_linear_led").
	MeasureKey string `gorm:"column:measure_key;not null;uniqueIndex:idx_pdf_raw_unique"`

	// PDFType is "catalog" or "application".
	PDFType string `gorm:"column:pdf_type;not null;uniqueIndex:idx_pdf_raw_unique"`

	// Pages is the human-readable page reference (e.g. "p.50", "p.13-14").
	Pages string `gorm:"column:pages;not null"`

	// FilePath is the absolute path of the PDF file that was read.
	FilePath string `gorm:"column:file_path;not null"`

	// RawText is the plain text extracted from the PDF pages.
	RawText string `gorm:"column:raw_text;type:text;not null"`

	// ScrapedAt is the time the extraction ran.
	ScrapedAt time.Time `gorm:"column:scraped_at;not null"`
}

// TableName tells GORM which table to use.  The schema prefix comes from
// ScraperSchema (set at startup from SCRAPER_DB_SCHEMA env var, default "scraper")
// so the table lives in the Go-owned schema and is invisible to Prisma.
func (PDFScrapeRaw) TableName() string { return ScraperSchema + ".pdf_scrape_raw" }
