package brokerlog

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// The broker owns this table (rebate-finder-broker prisma/schema.prisma,
// model SystemEvent). We write rows into it; we never migrate it. That is the
// same boundary the broker keeps in the other direction against scraper.*.
const eventTable = "broker.system_events"

// DisableEnv turns the whole path off for an operator who wants a collector to
// stop writing to the shared event log without a redeploy.
const DisableEnv = "BROKER_EVENT_LOG"

// New builds a writer against the staging database the collector is already
// connected to, or returns nil if it should not run — which callers do not
// need to check, because every method is nil-safe.
//
// It returns nil when the operator disabled it, and when broker.system_events
// does not exist. That second case is the one worth having: a collector
// deployed against a database where the broker's migrations have not run yet
// would otherwise log a failed INSERT every two seconds forever. One probe at
// startup turns that into one warning.
func New(gdb *gorm.DB, log *zap.Logger, opts Options) *Writer {
	if log == nil {
		log = zap.NewNop()
	}
	if gdb == nil {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv(DisableEnv)), "off") {
		log.Info("brokerlog: disabled by " + DisableEnv + "=off")
		return nil
	}

	var reg sql.NullString
	if err := gdb.Raw("SELECT to_regclass(?)", eventTable).Scan(&reg).Error; err != nil {
		log.Warn("brokerlog: could not check for the broker event table — collector events will not be shipped",
			zap.String("table", eventTable), zap.Error(err))
		return nil
	}
	if !reg.Valid {
		log.Warn("brokerlog: broker event table not present — collector events will not be shipped",
			zap.String("table", eventTable))
		return nil
	}

	return NewWithSink(&gormSink{db: gdb}, log, opts)
}

type gormSink struct {
	db *gorm.DB
}

// Write inserts the whole batch in one statement. One round-trip per batch is
// the entire reason this package buffers.
func (s *gormSink) Write(ctx context.Context, entries []Entry) error {
	if len(entries) == 0 {
		return nil
	}

	var sb strings.Builder
	sb.WriteString("INSERT INTO " + eventTable +
		" (at, level, category, action, message, correlation_id, actor, tenant_id, data) VALUES ")

	args := make([]any, 0, len(entries)*5)
	for i, e := range entries {
		if i > 0 {
			sb.WriteString(",")
		}
		// correlation_id, actor and tenant_id are literal NULLs, not
		// parameters. tenant_id in particular: there is no argument here that
		// could ever carry a tenant identity, which is the invariant made
		// structural rather than remembered.
		sb.WriteString("(?, ?, ?, ?, ?, NULL, NULL, NULL, ?::jsonb)")

		at := e.At
		if at.IsZero() {
			at = time.Now().UTC()
		}
		args = append(args, at.UTC(), normalizeLevel(e.Level), Category, truncate(e.Action, 120), truncate(e.Message, 2000), encodeData(e.Data))
	}

	if err := s.db.WithContext(ctx).Exec(sb.String(), args...).Error; err != nil {
		return fmt.Errorf("brokerlog: insert %d events: %w", len(entries), err)
	}
	return nil
}

func normalizeLevel(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case LevelDebug, LevelWarn, LevelError:
		return strings.ToLower(strings.TrimSpace(level))
	default:
		return LevelInfo
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// encodeData returns nil (a SQL NULL) rather than "null" for an empty payload,
// and drops a payload it cannot serialise rather than failing the batch that
// contains it.
func encodeData(data map[string]any) any {
	if len(data) == 0 {
		return nil
	}
	b, err := json.Marshal(data)
	if err != nil {
		return nil
	}
	if len(b) > 4000 {
		// Bounded like the broker's own ingest path: an enormous payload is
		// replaced by its measurement, because every row here is read back.
		b, err = json.Marshal(map[string]any{"_truncated": true, "_bytes": len(b)})
		if err != nil {
			return nil
		}
	}
	return string(b)
}
