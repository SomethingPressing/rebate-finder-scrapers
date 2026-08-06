package main

import (
	"fmt"

	"github.com/incenva/rebate-scraper/config"
	"github.com/incenva/rebate-scraper/db"
	"github.com/incenva/rebate-scraper/models"
	"go.uber.org/zap"
)

// runBackfillContentHash computes content_hash for every staging row that does
// not have one yet (rows written before the column existed).
//
// Idempotent: only rows with an empty content_hash are touched, so re-running
// is a no-op. Staging database only — no tenant database is contacted.
//
// v0.8 Feature 1: gives the promoter's shadow comparison a baseline to compare
// against instead of a wall of "hash missing".
func runBackfillContentHash(cfg *config.Config, logger *zap.Logger, dry bool) error {
	const batchSize = 500

	stagingDB, err := db.Connect(cfg.DatabaseURL, cfg.LogLevel, cfg.ScraperDBSchema)
	if err != nil {
		return fmt.Errorf("connect staging db: %w", err)
	}
	defer stagingDB.Close() //nolint:errcheck

	gormDB := stagingDB.GORM()
	table := models.ScraperSchema + ".rebates_staging"

	var pending int64
	if err := gormDB.Table(table).
		Where("content_hash IS NULL OR content_hash = ''").
		Count(&pending).Error; err != nil {
		return fmt.Errorf("count rows missing content_hash: %w", err)
	}

	logger.Info("content_hash backfill",
		zap.Int64("rows_missing_hash", pending),
		zap.Bool("dry_run", dry))

	if pending == 0 || dry {
		return nil
	}

	// Keyset-paginate by id (not OFFSET) so each pass re-reads only rows that
	// still lack a hash, and updated rows naturally fall out of the predicate.
	var updated int64
	lastID := uint(0)
	for {
		var rows []models.StagedRebate
		if err := gormDB.Table(table).
			Where("(content_hash IS NULL OR content_hash = '') AND id > ?", lastID).
			Order("id ASC").
			Limit(batchSize).
			Find(&rows).Error; err != nil {
			return fmt.Errorf("fetch batch after id %d: %w", lastID, err)
		}
		if len(rows) == 0 {
			break
		}

		for i := range rows {
			hash := models.ComputeContentHash(&rows[i])
			if err := gormDB.Table(table).
				Where("id = ?", rows[i].ID).
				Update("content_hash", hash).Error; err != nil {
				return fmt.Errorf("update row %d: %w", rows[i].ID, err)
			}
			updated++
			lastID = rows[i].ID
		}

		logger.Info("content_hash backfill progress",
			zap.Int64("updated", updated),
			zap.Int64("total", pending))
	}

	logger.Info("content_hash backfill complete", zap.Int64("updated", updated))
	return nil
}
