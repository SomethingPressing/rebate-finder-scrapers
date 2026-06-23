// Package jobsync is a thin HTTP client that calls the Next.js /api/upload-jobs
// endpoints so the Ingestion Monitor can track scraper and promoter runs.
// All methods are safe no-ops when AppURL or SyncSecret are empty.
package jobsync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Client calls the Next.js upload-jobs API with bearer-token auth.
type Client struct {
	appURL string
	secret string
	http   *http.Client
}

// New returns a Client. When appURL or secret is empty every method is a no-op.
func New(appURL, secret string) *Client {
	return &Client{
		appURL: strings.TrimRight(appURL, "/"),
		secret: secret,
		http:   &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *Client) enabled() bool { return c.appURL != "" && c.secret != "" }

// CreateJobRequest maps to POST /api/upload-jobs.
type CreateJobRequest struct {
	Type        string     `json:"type"`
	Source      string     `json:"source,omitempty"`
	RowCount    *int       `json:"row_count,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`   // → status processing
	ScheduledAt *time.Time `json:"scheduled_at,omitempty"` // → status scheduled
}

// UpdateJobRequest maps to PATCH /api/upload-jobs/:id.
type UpdateJobRequest struct {
	Status        string     `json:"status,omitempty"`
	RowCount      *int       `json:"row_count,omitempty"`
	RowsProcessed *int       `json:"rows_processed,omitempty"`
	RowsSkipped   *int       `json:"rows_skipped,omitempty"`
	ErrorCount    *int       `json:"error_count,omitempty"`
	ErrorMessage  *string    `json:"error_message,omitempty"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
}

// CreateJob registers a new upload job and returns its ID.
// Returns ("", nil) when the client is disabled so callers need no nil-guard.
func (c *Client) CreateJob(ctx context.Context, req CreateJobRequest) (string, error) {
	if !c.enabled() {
		return "", nil
	}
	return c.post(ctx, "/api/upload-jobs", req)
}

// UpdateJob patches an existing job. No-op when jobID is empty or client disabled.
func (c *Client) UpdateJob(ctx context.Context, jobID string, req UpdateJobRequest) error {
	if !c.enabled() || jobID == "" {
		return nil
	}
	_, err := c.patch(ctx, "/api/upload-jobs/"+jobID, req)
	return err
}

// ── HTTP helpers ──────────────────────────────────────────────────────────────

func (c *Client) post(ctx context.Context, path string, body any) (string, error) {
	return c.doRequest(ctx, http.MethodPost, path, body)
}

func (c *Client) patch(ctx context.Context, path string, body any) (string, error) {
	return c.doRequest(ctx, http.MethodPatch, path, body)
}

func (c *Client) doRequest(ctx context.Context, method, path string, body any) (string, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("jobsync: marshal %s %s: %w", method, path, err)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.appURL+path, bytes.NewReader(encoded))
	if err != nil {
		return "", fmt.Errorf("jobsync: build request %s %s: %w", method, path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.secret)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("jobsync: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("jobsync: %s %s → %d", method, path, resp.StatusCode)
	}

	var result struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&result)
	return result.ID, nil
}

// ── Convenience helpers ───────────────────────────────────────────────────────

func IntPtr(v int) *int       { return &v }
func StrPtr(v string) *string { return &v }
func TimePtr(v time.Time) *time.Time { return &v }
