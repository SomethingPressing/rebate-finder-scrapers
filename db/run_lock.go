package db

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

// The lock calls are short and must not inherit a request's cancellation:
// releasing is exactly what has to happen when everything else is shutting down.
func ctxBackground() context.Context { return context.Background() }

// One collection pass at a time, fleet-wide.
//
// ── Why this exists ──────────────────────────────────────────────────────────
//
// Collection is scheduled by the collector's own cron (decided 2026-08-25 —
// the alternative was Fly's machine scheduler, which would have put the
// timetable in a control plane neither repository can read and left the
// broker's "Collection interval" setting reaching nothing).
//
// That decision has one dangerous edge. If a Fly schedule is ALSO left in
// place, two things start collection: the standing machine's cron, and a
// machine Fly wakes on its own timetable. Nothing about that failure is loud —
// both passes work, both write to the same staging table, and the result is
// double the upstream requests against sources that rate-limit, for no extra
// data. It would show up as unexplained 429s days later.
//
// So the migration does not rely on somebody remembering to delete the Fly
// schedule. Two passes can be started; only one runs.
//
// ── Why an advisory lock ─────────────────────────────────────────────────────
//
// The lock has to live where every collector can see it, and the one thing
// they all share is the staging database. A Postgres advisory lock is free, is
// held on a session rather than a row, and — the property that matters — is
// released automatically when the connection dies. A lock stored in a table
// would survive a killed machine and block collection until somebody cleared
// it by hand, which is a worse failure than the one it prevents.
const collectionLockID int64 = 8_147_320_461

// TryLockCollection reports whether this process may start a collection pass.
//
// Returns false when another collector already holds the lock. That is a
// normal outcome, not an error: it means the fleet is already doing the work.
//
// The lock is held on ONE pooled connection, so the caller must keep the
// returned release function and call it when the pass ends — see UnlockCollection.
func (d *DB) TryLockCollection() (bool, error) {
	sqlDB, err := d.gorm.DB()
	if err != nil {
		return false, err
	}
	// A dedicated connection, because an advisory lock belongs to the session
	// that took it. Taken from the pool and handed back on unlock; without
	// this the lock could be released by whichever connection happened to run
	// the unlock, or held forever by one that returned to the pool.
	conn, err := sqlDB.Conn(ctxBackground())
	if err != nil {
		return false, err
	}

	var got bool
	if err := conn.QueryRowContext(ctxBackground(), "SELECT pg_try_advisory_lock($1)", collectionLockID).Scan(&got); err != nil {
		_ = conn.Close()
		return false, err
	}
	if !got {
		_ = conn.Close()
		return false, nil
	}

	d.lockMu.Lock()
	d.lockConn = conn
	d.lockMu.Unlock()
	return true, nil
}

// UnlockCollection releases the pass lock. Safe to call when nothing is held.
func (d *DB) UnlockCollection() error {
	d.lockMu.Lock()
	conn := d.lockConn
	d.lockConn = nil
	d.lockMu.Unlock()
	if conn == nil {
		return nil
	}
	_, err := conn.ExecContext(ctxBackground(), "SELECT pg_advisory_unlock($1)", collectionLockID)
	// Returning the connection to the pool also drops the lock if the unlock
	// itself failed, so a broken release cannot wedge the fleet.
	if cerr := conn.Close(); err == nil {
		err = cerr
	}
	if err != nil && errors.Is(err, gorm.ErrInvalidDB) {
		return nil
	}
	return err
}
