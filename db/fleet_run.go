package db

import (
	"os"
	"time"

	"github.com/incenva/rebate-scraper/models"
)

// Fleet telemetry — v0.8 Feature 4.
//
// Under shared collection a run is not performed for any one tenant, so the
// per-client run log stops describing reality. These helpers record the same
// events fleet-wide, with no tenant identity attached.
//
// They never return an error that stops a collection run: telemetry that can
// fail a scrape is worse than no telemetry. Failures are reported to the
// caller to log and otherwise ignored.

// StartFleetRun records that a collector began working on a source.
func (d *DB) StartFleetRun(source, triggeredBy, scraperVersion string, envelopeVersion *int64) (uint, error) {
	host, _ := os.Hostname()
	run := models.FleetRun{
		Source:    source,
		Status:    models.FleetRunRunning,
		StartedAt: time.Now().UTC(),
	}
	if triggeredBy != "" {
		run.TriggeredBy = &triggeredBy
	}
	if scraperVersion != "" {
		run.ScraperVersion = &scraperVersion
	}
	if host != "" {
		run.Host = &host
	}
	run.EnvelopeVersion = envelopeVersion

	if err := d.gorm.Create(&run).Error; err != nil {
		return 0, err
	}
	return run.ID, nil
}

// FinishFleetRun closes a run out with its outcome.
func (d *DB) FinishFleetRun(runID uint, status string, programCount int, runErr error) error {
	if runID == 0 {
		return nil
	}
	now := time.Now().UTC()
	updates := map[string]interface{}{
		"status":        status,
		"finished_at":   now,
		"program_count": programCount,
	}
	if runErr != nil {
		msg := runErr.Error()
		updates["error"] = msg
	}

	var run models.FleetRun
	if err := d.gorm.First(&run, runID).Error; err == nil {
		updates["duration_s"] = int(now.Sub(run.StartedAt).Seconds())
	}

	return d.gorm.Model(&models.FleetRun{}).Where("id = ?", runID).Updates(updates).Error
}

// ResetStalledFleetRuns closes out runs left "running" by a crashed process,
// so a killed collector does not look like it is still working forever.
//
// A PM2 restart, an OOM kill or a SIGKILL all skip FinishFleetRun, leaving the
// row "running" with no finished_at. Nothing else ever reaps it, so the
// broker's Health page reports that collector as "collecting now" indefinitely
// — confidently wrong, which is worse than saying nothing.
//
// finished_at is set as well as status. Without it the row reads as a run that
// never reported, and the health page says "has never reported a run — is this
// collector deployed?", which sends somebody to check a deployment when the
// real story is that a run started and died.
func (d *DB) ResetStalledFleetRuns(olderThan time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-olderThan)
	now := time.Now().UTC()
	res := d.gorm.Model(&models.FleetRun{}).
		Where("status = ? AND started_at < ?", models.FleetRunRunning, cutoff).
		Updates(map[string]interface{}{
			"status":      models.FleetRunError,
			"finished_at": now,
			"error":       "run did not finish — the collector stopped or crashed before reporting",
		})
	return res.RowsAffected, res.Error
}
