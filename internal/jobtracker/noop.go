package jobtracker

import "context"

// NoopTracker satisfies Tracker with no-ops. Returned by NewDB when given a nil
// *gorm.DB so callers never panic when job tracking is unavailable.
type NoopTracker struct{}

func (NoopTracker) CreateJob(context.Context, CreateJobRequest) (string, error) {
	return "", nil
}

func (NoopTracker) UpdateJob(context.Context, string, UpdateJobRequest) error {
	return nil
}
