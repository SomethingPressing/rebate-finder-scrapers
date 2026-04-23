package db

import (
	"fmt"
	"time"

	"github.com/incenva/rebate-scraper/models"
	"gorm.io/gorm/clause"
)

// PDFRawEntry is the input shape for UpsertPDFRaw — one extracted page range.
type PDFRawEntry struct {
	Source     string // e.g. "consumers_energy_pdf"
	MeasureKey string // e.g. "hvac_air_conditioning"
	PDFType    string // "catalog" or "application"
	Pages      string // e.g. "p.50"
	FilePath   string // absolute path of the source PDF
	RawText    string // plain text from the PDF pages
}

// UpsertPDFRaw writes raw PDF text extracts into the pdf_scrape_raw table.
//
// Each entry is identified by (source, measure_key, pdf_type).
// On conflict the text and metadata are refreshed; no duplicate rows are
// created for the same extract on subsequent scraper runs.
func UpsertPDFRaw(d *DB, entries []PDFRawEntry) error {
	if len(entries) == 0 {
		return nil
	}

	now := time.Now().UTC()
	rows := make([]models.PDFScrapeRaw, len(entries))
	for i, e := range entries {
		rows[i] = models.PDFScrapeRaw{
			Source:     e.Source,
			MeasureKey: e.MeasureKey,
			PDFType:    e.PDFType,
			Pages:      e.Pages,
			FilePath:   e.FilePath,
			RawText:    e.RawText,
			ScrapedAt:  now,
		}
	}

	// On conflict (source, measure_key, pdf_type): update everything except
	// created_at.  This means re-running the scraper always gives you the
	// freshest text without creating duplicate rows.
	result := d.gorm.
		Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "source"},
				{Name: "measure_key"},
				{Name: "pdf_type"},
			},
			DoUpdates: clause.AssignmentColumns([]string{
				"pages", "file_path", "raw_text", "scraped_at", "updated_at",
			}),
		}).
		Create(&rows)

	if result.Error != nil {
		return fmt.Errorf("upsert pdf_scrape_raw: %w", result.Error)
	}
	return nil
}
