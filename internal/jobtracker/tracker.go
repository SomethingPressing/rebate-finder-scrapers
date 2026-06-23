// Package jobtracker records scraper and promoter runs in the Ingestion Monitor
// by writing directly to the public.upload_jobs PostgreSQL table (managed by the
// Next.js Prisma schema), instead of going through the HTTP upload-jobs API.
//
// Use NewDB to obtain a DB-backed Tracker. All implementations are safe no-ops
// when disabled (e.g. a nil DB), so callers never need a nil-guard.
package jobtracker

import (
	"context"
	"time"
)

// Tracker registers and updates Ingestion Monitor jobs.
type Tracker interface {
	CreateJob(ctx context.Context, req CreateJobRequest) (string, error)
	UpdateJob(ctx context.Context, jobID string, req UpdateJobRequest) error
}

// CreateJobRequest describes a new upload_jobs row.
type CreateJobRequest struct {
	Type        string     // → "UploadJobType" (scraper_run | promoter_run | csv_import)
	Source      string     // optional source label
	RowCount    *int       // optional total row count
	StartedAt   *time.Time // → status processing
	ScheduledAt *time.Time // → status scheduled
}

// UpdateJobRequest patches an existing upload_jobs row. Only non-nil/non-zero
// fields are applied.
type UpdateJobRequest struct {
	Status        string     // → "UploadJobStatus"
	RowsProcessed *int
	RowsSkipped   *int
	ErrorCount    *int
	ErrorMessage  *string
	CompletedAt   *time.Time
}

// ── Convenience helpers ───────────────────────────────────────────────────────

func IntPtr(v int) *int               { return &v }
func StrPtr(v string) *string         { return &v }
func TimePtr(v time.Time) *time.Time  { return &v }
