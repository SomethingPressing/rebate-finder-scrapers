// Package brokerlog ships collector events to the broker's system event log
// (broker.system_events) so one administrator reading one table sees the whole
// story: what the broker did, what each tenant site did, and what the fleet
// collected.
//
// Why a direct write and not the HTTP route the tenant app uses. A collector
// already holds the staging DATABASE_URL — broker.system_events lives in that
// same database — so an INSERT costs one round-trip on an open pool where a
// POST would cost a connection, a TLS handshake, a JSON encode and a place in
// somebody's rate-limit budget. The tenant app has no such access by design
// and must use the wire contract; the collector does, and shouldn't pretend
// otherwise.
//
// Two invariants this package enforces rather than documents:
//
//   - tenant_id is always NULL. Collectors are tenant-blind: a run is
//     performed for the fleet, not for any one customer, and a collector must
//     not be able to tell whether one tenant or ten asked for a ZIP code.
//     Nothing in this package accepts a tenant id, so nothing can set one.
//   - category is always "collector". A collector cannot write lines that
//     appear to come from the broker or from a tenant.
//
// And the rule the whole design serves: logging must never fail a scrape. Every
// path here is asynchronous and best-effort. A full buffer drops and counts;
// a failing flush reports to stderr through zap and carries on. There is no
// path from this package that returns an error to a scraper.
package brokerlog

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// Category and tenant attribution are constants, not parameters. See above.
const (
	Category = "collector"

	LevelDebug = "debug"
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// Defaults chosen so a normal fleet run flushes a handful of times: batches
// are small enough that a crash loses little, and frequent enough that a
// watching administrator sees a run appear while it is still running.
const (
	DefaultBatchSize = 50
	DefaultInterval  = 2 * time.Second
	// Room for a burst without blocking. Beyond this we drop, because the
	// alternative is a scraper goroutine waiting on a log write.
	DefaultBufferSize = 1024
	// A flush that cannot finish in this long is not worth the collector's
	// shutdown time.
	flushTimeout = 10 * time.Second
)

// Entry is one line. Data is optional structured detail and must never carry a
// credential, a connection string or a session token — this table is readable
// by every administrator.
type Entry struct {
	Level   string
	Action  string
	Message string
	At      time.Time
	Data    map[string]any
}

// Sink is where a flushed batch goes. The production implementation writes to
// broker.system_events; tests substitute their own, which is what makes the
// batching and dropping behaviour testable without a database.
type Sink interface {
	Write(ctx context.Context, entries []Entry) error
}

// Options tunes a Writer. The zero value is usable — every field falls back to
// the package default.
type Options struct {
	BatchSize  int
	Interval   time.Duration
	BufferSize int
}

// Writer is a buffered, asynchronous shipper. Construct it with New or
// NewWithSink; a nil *Writer is a valid no-op writer, so a collector running
// without a reachable event table needs no branching at the call sites.
type Writer struct {
	sink Sink
	log  *zap.Logger

	entries chan Entry
	flushes chan chan struct{}
	quit    chan struct{}
	wg      sync.WaitGroup

	closed atomic.Bool
	// Counters, read back by Stats for the shutdown line.
	dropped  atomic.Int64
	written  atomic.Int64
	failed   atomic.Int64
	batchCnt atomic.Int64

	batchSize int
	interval  time.Duration
}

// NewWithSink starts a writer against any sink. The goroutine runs until Close.
func NewWithSink(sink Sink, log *zap.Logger, opts Options) *Writer {
	if sink == nil {
		return nil
	}
	if log == nil {
		log = zap.NewNop()
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = DefaultBatchSize
	}
	if opts.Interval <= 0 {
		opts.Interval = DefaultInterval
	}
	if opts.BufferSize <= 0 {
		opts.BufferSize = DefaultBufferSize
	}

	w := &Writer{
		sink:      sink,
		log:       log,
		entries:   make(chan Entry, opts.BufferSize),
		flushes:   make(chan chan struct{}),
		quit:      make(chan struct{}),
		batchSize: opts.BatchSize,
		interval:  opts.Interval,
	}
	w.wg.Add(1)
	go w.run()
	return w
}

// Log queues one line. It never blocks and never fails: if the buffer is full
// the line is dropped and counted, because a scrape waiting on its own logging
// is a worse outcome than a missing log line.
//
// Safe on a nil receiver and after Close — both are silent no-ops.
func (w *Writer) Log(level, action, message string, data map[string]any) {
	if w == nil || w.closed.Load() {
		return
	}
	entry := Entry{
		Level:   level,
		Action:  action,
		Message: message,
		At:      time.Now().UTC(),
		Data:    data,
	}
	select {
	case w.entries <- entry:
	default:
		// Deliberately not a zap line: a full buffer means the process is
		// already producing more than it can ship, and one stderr line per
		// drop would make that worse. The count surfaces in Stats.
		w.dropped.Add(1)
	}
}

// Info, Warn and Error are the shorthands the call sites actually use.
func (w *Writer) Info(action, message string, data map[string]any) {
	w.Log(LevelInfo, action, message, data)
}

func (w *Writer) Warn(action, message string, data map[string]any) {
	w.Log(LevelWarn, action, message, data)
}

func (w *Writer) Error(action, message string, data map[string]any) {
	w.Log(LevelError, action, message, data)
}

// Flush blocks until everything queued at the moment of the call has been
// handed to the sink. For the end of a run, where the next thing that happens
// might be the process exiting.
func (w *Writer) Flush() {
	if w == nil || w.closed.Load() {
		return
	}
	done := make(chan struct{})
	select {
	case w.flushes <- done:
		<-done
	case <-w.quit:
	}
}

// Close stops the writer after one final flush. Idempotent; safe on nil.
func (w *Writer) Close() {
	if w == nil || !w.closed.CompareAndSwap(false, true) {
		return
	}
	close(w.quit)
	w.wg.Wait()
}

// Stats reports what the writer did, for one line at shutdown. A non-zero
// Dropped or Failed is the signal that the fleet is producing more telemetry
// than this path can carry.
type Stats struct {
	Written int64
	Dropped int64
	Failed  int64
	Batches int64
}

func (w *Writer) Stats() Stats {
	if w == nil {
		return Stats{}
	}
	return Stats{
		Written: w.written.Load(),
		Dropped: w.dropped.Load(),
		Failed:  w.failed.Load(),
		Batches: w.batchCnt.Load(),
	}
}

// run is the single consumer. Everything that touches `batch` happens here, so
// the batch itself needs no lock.
func (w *Writer) run() {
	defer w.wg.Done()

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	batch := make([]Entry, 0, w.batchSize)

	flush := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
		err := w.sink.Write(ctx, batch)
		cancel()

		w.batchCnt.Add(1)
		if err != nil {
			// Report and move on. A broker whose event table is unreachable
			// must not stop a collection run.
			w.failed.Add(int64(len(batch)))
			w.log.Warn("brokerlog: could not ship a batch of collector events",
				zap.Int("entries", len(batch)), zap.Error(err))
		} else {
			w.written.Add(int64(len(batch)))
		}
		// Reuse the backing array; the batch is bounded by batchSize plus
		// whatever one drain adds, so this does not grow without limit.
		batch = batch[:0]
	}

	// drain moves everything currently buffered into the batch, flushing
	// whenever the batch fills, so a burst larger than one batch still goes
	// out in batch-sized pieces rather than one enormous statement.
	drain := func() {
		for {
			select {
			case e := <-w.entries:
				batch = append(batch, e)
				if len(batch) >= w.batchSize {
					flush()
				}
			default:
				return
			}
		}
	}

	for {
		select {
		case e := <-w.entries:
			batch = append(batch, e)
			if len(batch) >= w.batchSize {
				flush()
			}

		case done := <-w.flushes:
			drain()
			flush()
			close(done)

		case <-ticker.C:
			flush()

		case <-w.quit:
			drain()
			flush()
			return
		}
	}
}
