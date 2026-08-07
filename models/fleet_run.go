package models

import "time"

// FleetRunStatus values for FleetRun.Status.
const (
	FleetRunRunning = "running"
	FleetRunSuccess = "success"
	FleetRunError   = "error"
)

// FleetRun records one collection run by one collector, fleet-wide.
//
// It exists because `scraper_run_logs` cannot survive shared collection: that
// table is keyed by client_id, and under the v0.8 architecture a run is not
// performed *for* a client at all. One run serves everybody who asked for that
// source, so "your last run" stops being a meaningful question and "when did
// the fleet last collect this source?" takes its place (spec §6.2).
//
// This lives in the scraper schema because the collectors own it — they are
// the only writers. The broker reads it to answer the queue endpoint's info
// route, and tenants see it as read-only telemetry they must never branch on:
// a tenant that stopped draining because a collector looked stale would be
// making a delivery decision from an observability signal.
type FleetRun struct {
	ID         uint       `gorm:"primarykey;autoIncrement"`
	Source     string     `gorm:"column:source;not null;index"`
	Status     string     `gorm:"column:status;not null;index"`
	StartedAt  time.Time  `gorm:"column:started_at;not null"`
	FinishedAt *time.Time `gorm:"column:finished_at"`
	// ProgramCount is what this run staged, not what any tenant received.
	ProgramCount *int    `gorm:"column:program_count"`
	DurationS    *int    `gorm:"column:duration_s"`
	Error        *string `gorm:"column:error"`
	TriggeredBy  *string `gorm:"column:triggered_by"`
	// ScraperVersion and Host make it possible to tell two collectors apart
	// when the fleet is split, without naming any tenant.
	ScraperVersion *string `gorm:"column:scraper_version"`
	Host           *string `gorm:"column:host"`
	// EnvelopeVersion is the demand envelope this run worked from, so a
	// collection gap can be traced to the instruction that produced it.
	EnvelopeVersion *int64 `gorm:"column:envelope_version"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

func (FleetRun) TableName() string { return ScraperSchema + ".fleet_runs" }
