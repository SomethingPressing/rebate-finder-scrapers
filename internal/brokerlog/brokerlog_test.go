package brokerlog

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

// fakeSink records every batch it is handed, so a test can assert on the
// batching itself rather than on what ended up in a database.
type fakeSink struct {
	mu      sync.Mutex
	batches [][]Entry
	err     error
	// block, when non-nil, holds Write until the test releases it — used to
	// keep the consumer goroutine busy while the buffer fills.
	block chan struct{}
}

func (f *fakeSink) Write(_ context.Context, entries []Entry) error {
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	// The writer reuses its backing array, so a batch must be copied to be
	// inspected later. (A sink that keeps the slice would see it mutate.)
	cp := make([]Entry, len(entries))
	copy(cp, entries)
	f.batches = append(f.batches, cp)
	return f.err
}

func (f *fakeSink) snapshot() [][]Entry {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]Entry, len(f.batches))
	copy(out, f.batches)
	return out
}

func (f *fakeSink) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, b := range f.batches {
		n += len(b)
	}
	return n
}

func TestFlushesWhenTheBatchFills(t *testing.T) {
	sink := &fakeSink{}
	// A long interval, so anything that arrives is the size trigger and not
	// the timer.
	w := NewWithSink(sink, zap.NewNop(), Options{BatchSize: 5, Interval: time.Hour, BufferSize: 100})
	defer w.Close()

	for i := 0; i < 10; i++ {
		w.Info("collector.run-started", "started", nil)
	}
	w.Flush()

	batches := sink.snapshot()
	if len(batches) != 2 {
		t.Fatalf("want 2 batches of 5, got %d batches", len(batches))
	}
	for i, b := range batches {
		if len(b) != 5 {
			t.Errorf("batch %d: want 5 entries, got %d", i, len(b))
		}
	}
}

func TestFlushesOnTheIntervalBeforeTheBatchFills(t *testing.T) {
	sink := &fakeSink{}
	w := NewWithSink(sink, zap.NewNop(), Options{BatchSize: 1000, Interval: 20 * time.Millisecond, BufferSize: 100})
	defer w.Close()

	w.Info("collector.run-started", "started", nil)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sink.total() == 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the timer never flushed a partial batch: got %d entries", sink.total())
}

func TestDropsInsteadOfBlockingWhenTheBufferIsFull(t *testing.T) {
	// The consumer is parked inside Write, so nothing is drained and the
	// buffer is the only thing absorbing the load. This is the backpressure
	// case: a scrape must not wait here.
	release := make(chan struct{})
	sink := &fakeSink{block: release}
	w := NewWithSink(sink, zap.NewNop(), Options{BatchSize: 1, Interval: time.Hour, BufferSize: 4})

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			w.Info("collector.source-error", "noise", nil)
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Log blocked when the buffer was full — it must drop instead")
	}

	if got := w.Stats().Dropped; got == 0 {
		t.Fatal("want a non-zero drop count once the buffer filled, got 0")
	}
	if got := w.Stats().Dropped; got >= 500 {
		t.Fatalf("everything was dropped (%d) — the buffer absorbed nothing", got)
	}

	close(release)
	w.Close()
}

func TestAFailingFlushIsCountedAndDoesNotStopTheWriter(t *testing.T) {
	sink := &fakeSink{err: errors.New("broker unreachable")}
	w := NewWithSink(sink, zap.NewNop(), Options{BatchSize: 2, Interval: time.Hour, BufferSize: 100})

	for i := 0; i < 4; i++ {
		w.Info("collector.run-finished", "finished", nil)
	}
	w.Flush()

	// Still accepting after the failures — the point of the whole design.
	w.Info("collector.run-finished", "and another", nil)
	w.Flush()
	w.Close()

	stats := w.Stats()
	if stats.Failed != 5 {
		t.Errorf("want 5 failed entries, got %d", stats.Failed)
	}
	if stats.Written != 0 {
		t.Errorf("want 0 written when every flush errored, got %d", stats.Written)
	}
	if stats.Dropped != 0 {
		t.Errorf("a flush error is not a drop; got %d dropped", stats.Dropped)
	}
}

func TestCloseFlushesWhatIsStillBuffered(t *testing.T) {
	sink := &fakeSink{}
	w := NewWithSink(sink, zap.NewNop(), Options{BatchSize: 1000, Interval: time.Hour, BufferSize: 100})

	for i := 0; i < 7; i++ {
		w.Info("collector.run-finished", "finished", nil)
	}
	w.Close() // no batch-size trigger, no timer tick: only Close can save these

	if got := sink.total(); got != 7 {
		t.Fatalf("want the 7 buffered entries flushed on Close, got %d", got)
	}
}

func TestCloseIsIdempotentAndLoggingAfterCloseIsANoOp(t *testing.T) {
	sink := &fakeSink{}
	w := NewWithSink(sink, zap.NewNop(), Options{BatchSize: 1, Interval: time.Hour, BufferSize: 10})

	w.Close()
	w.Close() // must not panic on a second close

	w.Info("collector.run-started", "after close", nil)
	w.Flush()

	if got := sink.total(); got != 0 {
		t.Fatalf("want nothing shipped after Close, got %d entries", got)
	}
}

func TestNilWriterIsASilentNoOp(t *testing.T) {
	// The shape every call site relies on: `var w *Writer` stays usable when
	// the event table is absent, so no scraper needs a nil check.
	var w *Writer
	w.Info("collector.run-started", "started", map[string]any{"source": "dsireusa"})
	w.Warn("collector.source-error", "failed", nil)
	w.Error("collector.source-error", "failed", nil)
	w.Flush()
	w.Close()
	if got := w.Stats(); got != (Stats{}) {
		t.Fatalf("want zero stats from a nil writer, got %+v", got)
	}
}

func TestEveryEntryCarriesTheCollectorAttribution(t *testing.T) {
	sink := &fakeSink{}
	w := NewWithSink(sink, zap.NewNop(), Options{BatchSize: 100, Interval: time.Hour, BufferSize: 100})

	w.Info("collector.run-started", "started", map[string]any{"source": "dsireusa"})
	w.Flush()
	w.Close()

	batches := sink.snapshot()
	if len(batches) != 1 || len(batches[0]) != 1 {
		t.Fatalf("want one entry in one batch, got %v", batches)
	}
	got := batches[0][0]
	if got.Action != "collector.run-started" {
		t.Errorf("action: got %q", got.Action)
	}
	if got.At.IsZero() {
		t.Error("want a timestamp stamped at Log time")
	}
	// There is no tenant field on Entry at all — the invariant is structural.
	// This asserts the level shorthand rather than re-asserting the absence.
	if got.Level != LevelInfo {
		t.Errorf("level: got %q, want %q", got.Level, LevelInfo)
	}
}

func TestConcurrentLoggersDoNotRaceOrLose(t *testing.T) {
	sink := &fakeSink{}
	w := NewWithSink(sink, zap.NewNop(), Options{BatchSize: 10, Interval: 5 * time.Millisecond, BufferSize: 4096})

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				w.Info("collector.run-finished", "finished", nil)
			}
		}()
	}
	wg.Wait()
	w.Close()

	stats := w.Stats()
	// The buffer is far larger than the load, so nothing should have dropped
	// and every line should be accounted for exactly once.
	if stats.Dropped != 0 {
		t.Errorf("want no drops with a 4096 buffer and 800 entries, got %d", stats.Dropped)
	}
	if total := sink.total(); total != 800 {
		t.Errorf("want all 800 entries shipped, got %d", total)
	}
	if stats.Written != 800 {
		t.Errorf("want 800 counted as written, got %d", stats.Written)
	}
}

func TestNormalizeLevelAndPayloadBounds(t *testing.T) {
	if got := normalizeLevel("WARN"); got != LevelWarn {
		t.Errorf("normalizeLevel(WARN) = %q", got)
	}
	if got := normalizeLevel("nonsense"); got != LevelInfo {
		t.Errorf("an unknown level must fall back to info, got %q", got)
	}
	if got := normalizeLevel(""); got != LevelInfo {
		t.Errorf("an empty level must fall back to info, got %q", got)
	}

	if got := encodeData(nil); got != nil {
		t.Errorf("empty data must become a SQL NULL, got %v", got)
	}
	if got := encodeData(map[string]any{"source": "dsireusa"}); got != `{"source":"dsireusa"}` {
		t.Errorf("encodeData = %v", got)
	}

	big := make([]byte, 8000)
	for i := range big {
		big[i] = 'x'
	}
	out, ok := encodeData(map[string]any{"blob": string(big)}).(string)
	if !ok || len(out) > 200 {
		t.Errorf("an oversized payload must be replaced by its measurement, got %d chars", len(out))
	}

	if got := truncate("abcdef", 3); got != "abc" {
		t.Errorf("truncate = %q", got)
	}
}
