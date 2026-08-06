package models

import "time"

// LinkReason values for RebateTenantStatus.LinkReason — why a link row is (or
// stopped being) part of a tenant's feed. Matches the architecture spec §6.3.
const (
	LinkInScope    = "in_scope"     // the program matches this tenant's declared scope
	LinkOutOfScope = "out_of_scope" // the program left this tenant's scope (not a withdrawal)
)

// RebateTenantStatus tracks per-tenant promotion state for each staging row.
// One row exists per (staging row, tenant) pair, written by the scraper when
// it tags an incentive for a tenant. The promoter reads and updates this table.
//
// This design allows the same incentive to be independently promoted (or not)
// to each tenant without duplicating staging data.
//
// v0.8 (shared data bots): the four queue columns below turn each link row
// into a per-tenant queue entry. The link table IS the queue — there is no
// separate queueing product. See rebate-finder
// docs/specifications/shared-scraper-hub.md §6.3.
type RebateTenantStatus struct {
	ID              uint       `gorm:"primarykey;autoIncrement"`
	StagingID       uint       `gorm:"column:staging_id;not null;uniqueIndex:idx_staging_tenant"`
	TenantID        string     `gorm:"column:tenant_id;not null;uniqueIndex:idx_staging_tenant"`
	PromotionStatus string     `gorm:"column:promotion_status;default:'pending';index"`
	PromotedAt      *time.Time `gorm:"column:promoted_at"`
	RebateID        *string    `gorm:"column:rebate_id"`

	// ── v0.8 queue columns ───────────────────────────────────────────────────

	// seq: this entry's position in the tenant's queue. Assigned exactly once,
	// by the promoter, and ONLY when the staging row is released
	// (stg_release_status = 'released'). NULL means "linked but not queued".
	// This rule is what makes cumulative acknowledgements safe: an unreleased
	// row has no sequence number, so an ack can never skip past it — when the
	// row is released later it enters the queue with a fresh, higher number.
	Seq *int64 `gorm:"column:seq"`

	// delivered_at: stamped when the tenant acknowledges past this entry's
	// seq. NULL while the entry is pending delivery.
	DeliveredAt *time.Time `gorm:"column:delivered_at"`

	// unlinked_at + link_reason: when and why this entry left the tenant's
	// feed. link_reason distinguishes "out_of_scope" (left the tenant's
	// territory — the tenant just drops it) from a source withdrawal, which is
	// signalled by source_removed_at on the staging row and must be archived
	// by the tenant. The two must never be conflated.
	UnlinkedAt *time.Time `gorm:"column:unlinked_at"`
	LinkReason *string    `gorm:"column:link_reason"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

func (RebateTenantStatus) TableName() string { return ScraperSchema + ".rebate_tenant_status" }
