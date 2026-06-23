package jobtracker

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// DBTracker writes Ingestion Monitor jobs straight into public.upload_jobs on
// the same PostgreSQL database the scrapers already use.
type DBTracker struct {
	db *gorm.DB
}

// NewDB returns a Tracker backed by the given GORM connection. When gdb is nil
// it returns a *NoopTracker so callers never panic.
func NewDB(gdb *gorm.DB) Tracker {
	if gdb == nil {
		return &NoopTracker{}
	}
	return &DBTracker{db: gdb}
}

// CreateJob inserts a new upload_jobs row and returns its generated UUID.
// Status is derived from the request: scheduled when ScheduledAt is set,
// processing when StartedAt is set, otherwise pending.
func (t *DBTracker) CreateJob(ctx context.Context, req CreateJobRequest) (string, error) {
	id := uuid.NewString()

	status := "pending"
	switch {
	case req.ScheduledAt != nil:
		status = "scheduled"
	case req.StartedAt != nil:
		status = "processing"
	}

	// Pass nil pointers directly so PostgreSQL stores NULL for unset fields.
	const q = `
		INSERT INTO public.upload_jobs
			(id, type, source, row_count, status, started_at, scheduled_at, created_at, updated_at)
		VALUES
			(?, ?::"UploadJobType", ?, ?, ?::"UploadJobStatus", ?, ?, NOW(), NOW())`

	err := t.db.WithContext(ctx).Exec(q,
		id,
		req.Type,
		nullStr(req.Source),
		req.RowCount,
		status,
		req.StartedAt,
		req.ScheduledAt,
	).Error
	if err != nil {
		return "", fmt.Errorf("jobtracker: insert upload_job: %w", err)
	}
	return id, nil
}

// UpdateJob applies a dynamic UPDATE built from the non-nil/non-zero fields of
// req. No-op when jobID is empty.
func (t *DBTracker) UpdateJob(ctx context.Context, jobID string, req UpdateJobRequest) error {
	if jobID == "" {
		return nil
	}

	sets := []string{"updated_at = NOW()"}
	args := []any{}

	if req.Status != "" {
		sets = append(sets, `status = ?::"UploadJobStatus"`)
		args = append(args, req.Status)
	}
	if req.RowsProcessed != nil {
		sets = append(sets, "rows_processed = ?")
		args = append(args, *req.RowsProcessed)
	}
	if req.RowsSkipped != nil {
		sets = append(sets, "rows_skipped = ?")
		args = append(args, *req.RowsSkipped)
	}
	if req.ErrorCount != nil {
		sets = append(sets, "error_count = ?")
		args = append(args, *req.ErrorCount)
	}
	if req.ErrorMessage != nil {
		sets = append(sets, "error_message = ?")
		args = append(args, *req.ErrorMessage)
	}
	if req.CompletedAt != nil {
		sets = append(sets, "completed_at = ?")
		args = append(args, *req.CompletedAt)
	}

	// Nothing but updated_at would change — skip the round trip.
	if len(sets) == 1 {
		return nil
	}

	q := "UPDATE public.upload_jobs SET " + joinComma(sets) + " WHERE id = ?"
	args = append(args, jobID)

	if err := t.db.WithContext(ctx).Exec(q, args...).Error; err != nil {
		return fmt.Errorf("jobtracker: update upload_job %s: %w", jobID, err)
	}
	return nil
}

// nullStr returns nil for an empty string so PostgreSQL stores NULL.
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}
