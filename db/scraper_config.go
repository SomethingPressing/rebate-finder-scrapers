package db

import (
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ScraperSourceConfigRow mirrors the scraper_source_configs table (public schema).
type ScraperSourceConfigRow struct {
	ClientID         string     `gorm:"column:client_id;primaryKey"`
	Source           string     `gorm:"column:source;primaryKey"`
	Active           bool       `gorm:"column:active"`
	Schedule         string     `gorm:"column:schedule"`
	States           StringSlice `gorm:"column:states;type:text[]"`
	Utilities        StringSlice `gorm:"column:utilities;type:text[]"`
	ServiceAreas     StringSlice `gorm:"column:service_areas;type:text[]"`
	ZipCodes         StringSlice `gorm:"column:zip_codes;type:text[]"`
	MaxIncentives    *int       `gorm:"column:max_incentives"`
	LastRunAt        *time.Time `gorm:"column:last_run_at"`
	LastRunStatus    string     `gorm:"column:last_run_status"`
	LastRunCount     *int       `gorm:"column:last_run_count"`
	LastRunError     *string    `gorm:"column:last_run_error"`
}

func (ScraperSourceConfigRow) TableName() string { return "scraper_source_configs" }

// ScraperJobRow mirrors the scraper_jobs table (public schema).
type ScraperJobRow struct {
	ID         string     `gorm:"column:id;primaryKey"`
	ClientID   string     `gorm:"column:client_id"`
	Source     string     `gorm:"column:source"`
	Status     string     `gorm:"column:status"`
	CreatedAt  time.Time  `gorm:"column:created_at"`
	PickedUpAt *time.Time `gorm:"column:picked_up_at"`
}

func (ScraperJobRow) TableName() string { return "scraper_jobs" }

// ScraperRunLogRow mirrors the scraper_run_logs table (public schema).
type ScraperRunLogRow struct {
	ID               string     `gorm:"column:id;primaryKey"`
	ClientID         string     `gorm:"column:client_id"`
	Source           string     `gorm:"column:source"`
	Status           string     `gorm:"column:status"`
	StartedAt        time.Time  `gorm:"column:started_at"`
	FinishedAt       *time.Time `gorm:"column:finished_at"`
	ProgramCount     *int       `gorm:"column:program_count"`
	DurationS        *int       `gorm:"column:duration_s"`
	Error            *string    `gorm:"column:error"`
	TriggeredBy      *string    `gorm:"column:triggered_by"`
	LastHeartbeatAt  *time.Time `gorm:"column:last_heartbeat_at"`
}

func (ScraperRunLogRow) TableName() string { return "scraper_run_logs" }

// ResetStalledRuns marks any rows stuck in 'running' state as 'error'.
// Called at scraper startup so a killed/crashed process never permanently
// locks a source — the next tick will treat the source as due normally.
func (d *DB) ResetStalledRuns() error {
	msg := "Run interrupted — scraper process was restarted"
	if err := d.gorm.Model(&ScraperSourceConfigRow{}).
		Where("last_run_status = ?", "running").
		Updates(map[string]interface{}{
			"last_run_status": "error",
			"last_run_error":  msg,
		}).Error; err != nil {
		return err
	}
	return d.gorm.Model(&ScraperRunLogRow{}).
		Where("status = ?", "running").
		Updates(map[string]interface{}{
			"status":      "error",
			"finished_at": time.Now(),
			"error":       msg,
		}).Error
}

// UpdateHeartbeat stamps last_heartbeat_at = now on a run log row.
// Called every 30 s by each active scraper goroutine.
func (d *DB) UpdateHeartbeat(runLogID string) error {
	return d.gorm.Model(&ScraperRunLogRow{}).
		Where("id = ?", runLogID).
		Update("last_heartbeat_at", time.Now()).Error
}

// ResetStalledByHeartbeat finds run logs stuck in 'running' whose heartbeat
// hasn't been updated since cutoff (5 min ago), marks them as error, and
// clears the corresponding source config status so the next tick re-runs them.
// Returns the list of freed source names.
func (d *DB) ResetStalledByHeartbeat(cutoff time.Time) ([]string, error) {
	// A run is stalled when:
	//   - status is still 'running', AND
	//   - either the heartbeat exists but is older than cutoff,
	//     OR no heartbeat has been sent yet but the run started before cutoff.
	const stalledCond = `status = 'running' AND (
		(last_heartbeat_at IS NOT NULL AND last_heartbeat_at < ?) OR
		(last_heartbeat_at IS NULL     AND started_at          < ?)
	)`

	var rows []ScraperRunLogRow
	if err := d.gorm.Where(stalledCond, cutoff, cutoff).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}

	sources := make([]string, len(rows))
	for i, r := range rows {
		sources[i] = r.Source
	}

	msg := "Run stalled — no heartbeat received for 5+ minutes"
	now := time.Now()
	if err := d.gorm.Model(&ScraperRunLogRow{}).
		Where(stalledCond, cutoff, cutoff).
		Updates(map[string]interface{}{
			"status":      "error",
			"finished_at": now,
			"error":       msg,
		}).Error; err != nil {
		return sources, err
	}
	if err := d.gorm.Model(&ScraperSourceConfigRow{}).
		Where("source IN ? AND last_run_status = 'running'", sources).
		Updates(map[string]interface{}{
			"last_run_status": "error",
			"last_run_error":  msg,
		}).Error; err != nil {
		return sources, err
	}
	return sources, nil
}

// LoadActiveSourceConfigs returns all active source configs from the DB.
// Returns nil (not error) if the table doesn't exist — enables graceful
// degradation when the admin app hasn't been set up yet.
func (d *DB) LoadActiveSourceConfigs() ([]ScraperSourceConfigRow, error) {
	var rows []ScraperSourceConfigRow
	result := d.gorm.Where("active = true").Find(&rows)
	if result.Error != nil {
		if strings.Contains(result.Error.Error(), "does not exist") {
			return nil, nil
		}
		return nil, result.Error
	}
	return rows, nil
}

// ClaimJob atomically finds one queued job for an *active* source and marks it
// as picked_up. Returns nil if no eligible job is waiting.
func (d *DB) ClaimJob() (*ScraperJobRow, error) {
	var job ScraperJobRow
	now := time.Now()
	err := d.gorm.Transaction(func(tx *gorm.DB) error {
		// Only claim jobs whose source is currently active in scraper_source_configs.
		// The JOIN means a job queued before the source was disabled will be silently
		// skipped until the source is re-enabled.
		result := tx.Raw(`
			SELECT j.* FROM scraper_jobs j
			JOIN scraper_source_configs c ON c.client_id = j.client_id AND c.source = j.source AND c.active = true
			WHERE j.status = 'queued'
			ORDER BY j.created_at ASC
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		`).Scan(&job)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return tx.Model(&job).Updates(map[string]interface{}{
			"status":       "picked_up",
			"picked_up_at": now,
		}).Error
	})
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		// Gracefully handle missing table
		if strings.Contains(err.Error(), "does not exist") {
			return nil, nil
		}
		return nil, err
	}
	return &job, nil
}

// MarkRunStart writes status=running on the source config and inserts a run log row.
// Returns the run log ID.
func (d *DB) MarkRunStart(clientID, source, triggeredBy string) (string, error) {
	now := time.Now()
	if err := d.gorm.Model(&ScraperSourceConfigRow{}).
		Where("client_id = ? AND source = ?", clientID, source).
		Updates(map[string]interface{}{
			"last_run_status": "running",
			"last_run_at":     now,
			"last_run_error":  nil,
		}).Error; err != nil {
		return "", err
	}
	runLog := ScraperRunLogRow{
		ID:          uuid.NewString(),
		ClientID:    clientID,
		Source:      source,
		Status:      "running",
		StartedAt:   now,
		TriggeredBy: &triggeredBy,
	}
	if err := d.gorm.Create(&runLog).Error; err != nil {
		return "", err
	}
	return runLog.ID, nil
}

// MarkRunFinish updates the source config and run log with final status.
func (d *DB) MarkRunFinish(clientID, source, runLogID, status string, count int, durationS int, runErr error) error {
	now := time.Now()
	errMsg := (*string)(nil)
	if runErr != nil {
		s := runErr.Error()
		errMsg = &s
	}
	if err := d.gorm.Model(&ScraperSourceConfigRow{}).
		Where("client_id = ? AND source = ?", clientID, source).
		Updates(map[string]interface{}{
			"last_run_status": status,
			"last_run_count":  count,
			"last_run_error":  errMsg,
		}).Error; err != nil {
		return err
	}
	return d.gorm.Model(&ScraperRunLogRow{}).
		Where("id = ?", runLogID).
		Updates(map[string]interface{}{
			"status":        status,
			"finished_at":   now,
			"program_count": count,
			"duration_s":    durationS,
			"error":         errMsg,
		}).Error
}
