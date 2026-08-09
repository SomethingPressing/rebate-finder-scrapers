package brokerlog

import (
	"database/sql"
	"encoding/json"
	"os"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// Liveness for a collector — v0.8.
//
// The broker cannot poll a collector: they run elsewhere, on their own
// schedule, with no inbound route. Before this, "is it online?" was answered
// from the last COMPLETED run, which cannot tell an idle collector from a dead
// one — between two six-hourly scrapes those look identical for six hours.
//
// So the collector says so itself, on a timer, for as long as its process is
// alive. A beat means "this process exists and can reach the database" and
// deliberately nothing more: whether its work is succeeding is what fleet_runs
// and the event log are for. Conflating the two would let a collector that
// fails every scrape report itself healthy.
//
// The broker owns this table; we write rows, we never migrate it.
const heartbeatTable = "broker.participant_heartbeats"

// HeartbeatDisableEnv turns beating off without a redeploy.
const HeartbeatDisableEnv = "BROKER_HEARTBEAT"

// heartbeatInterval is also written into each row, so the broker judges this
// collector against the cadence it actually promised rather than a global
// constant that would libel a slower participant.
const heartbeatInterval = 30 * time.Second

// Heart beats for one collector process until it is stopped.
type Heart struct {
	gdb      *gorm.DB
	log      *zap.Logger
	id       string
	interval time.Duration
	stop     chan struct{}
	done     chan struct{}
}

// StartHeart begins beating, or returns nil if it should not run — which
// callers need not check, because every method is nil-safe.
//
// Like the event log, it returns nil when the table is absent: a collector
// pointed at a database where the broker's migrations have not run would
// otherwise log a failed write every 30 seconds forever.
func StartHeart(gdb *gorm.DB, log *zap.Logger, id string) *Heart {
	if log == nil {
		log = zap.NewNop()
	}
	if gdb == nil || id == "" {
		return nil
	}
	if v := os.Getenv(HeartbeatDisableEnv); v == "0" || v == "false" || v == "off" {
		return nil
	}
	var reg sql.NullString
	if err := gdb.Raw("SELECT to_regclass(?)", heartbeatTable).Scan(&reg).Error; err != nil || !reg.Valid {
		log.Warn("brokerlog: heartbeat table not present — this collector will not report liveness",
			zap.String("table", heartbeatTable),
		)
		return nil
	}

	h := &Heart{
		gdb:      gdb,
		log:      log,
		id:       id,
		interval: heartbeatInterval,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	// Beat once immediately: a collector that starts and dies within the first
	// interval should still have been seen, not vanish without trace.
	h.beat()
	go h.loop()
	return h
}

func (h *Heart) loop() {
	defer close(h.done)
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			h.beat()
		case <-h.stop:
			return
		}
	}
}

func (h *Heart) beat() {
	if h == nil {
		return
	}
	host, _ := os.Hostname()
	detail := map[string]any{
		"host":    host,
		"pid":     os.Getpid(),
		"version": os.Getenv("SCRAPER_VERSION"),
	}
	payload, err := json.Marshal(detail)
	if err != nil {
		return
	}

	// stopped_at is cleared on every beat: a process that beats again has
	// evidently come back, and a stale goodbye must not outrank a live beat.
	sql := `
		INSERT INTO ` + heartbeatTable + ` (kind, id, last_seen_at, interval_s, stopped_at, detail, first_seen_at)
		VALUES ('collector', ?, now(), ?, NULL, ?::jsonb, now())
		ON CONFLICT (kind, id) DO UPDATE SET
		  last_seen_at = now(), interval_s = EXCLUDED.interval_s,
		  stopped_at = NULL, detail = EXCLUDED.detail`
	if err := h.gdb.Exec(sql, h.id, int(h.interval.Seconds()), string(payload)).Error; err != nil {
		// Never fatal, never retried: a heartbeat that can stop a collection
		// run is worse than no heartbeat at all.
		h.log.Warn("heartbeat write failed", zap.Error(err))
	}
}

// Stop ends beating and records a clean shutdown, so the console shows
// "stopped" rather than making somebody wait out a timeout to learn the
// difference between a deliberate stop and a crash.
func (h *Heart) Stop() {
	if h == nil {
		return
	}
	close(h.stop)
	<-h.done
	sql := `UPDATE ` + heartbeatTable + ` SET stopped_at = now() WHERE kind = 'collector' AND id = ?`
	if err := h.gdb.Exec(sql, h.id).Error; err != nil {
		h.log.Warn("could not record collector shutdown", zap.Error(err))
	}
}
