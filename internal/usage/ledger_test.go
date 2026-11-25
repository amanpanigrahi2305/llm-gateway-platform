package usage

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"
)

// fakeWriter records batch sizes. If gate is set, each write waits for it.
type fakeWriter struct {
	mu    sync.Mutex
	sizes []int
	err   error
	gate  chan struct{}
}

func (f *fakeWriter) WriteBatch(_ context.Context, recs []Record) error {
	if f.gate != nil {
		<-f.gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sizes = append(f.sizes, len(recs))
	return f.err
}

func (f *fakeWriter) batches() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.sizes)
}

var quiet = slog.New(slog.DiscardHandler)

func TestLedgerWritesInBatches(t *testing.T) {
	w := &fakeWriter{}
	l := NewLedger(w, LedgerOptions{BufferSize: 100, BatchSize: 3, FlushInterval: time.Hour}, quiet)
	for range 7 {
		l.Write(Record{RequestID: "r"})
	}
	if err := l.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := w.batches(); !slices.Equal(got, []int{3, 3, 1}) {
		t.Errorf("batches = %v, want [3 3 1] (the last one written at Close)", got)
	}
	if s := l.Stats(); s.Written != 7 || s.Dropped != 0 || s.Failed != 0 {
		t.Errorf("stats = %+v", s)
	}
}

func TestLedgerFlushesPartialBatchOnTimer(t *testing.T) {
	w := &fakeWriter{}
	l := NewLedger(w, LedgerOptions{BufferSize: 100, BatchSize: 100, FlushInterval: 20 * time.Millisecond}, quiet)
	defer l.Close(context.Background())
	l.Write(Record{})
	l.Write(Record{})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if slices.Equal(w.batches(), []int{2}) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("batches = %v; want 2 records written by the timer", w.batches())
}

func TestLedgerDropsWhenBufferIsFull(t *testing.T) {
	w := &fakeWriter{gate: make(chan struct{})} // the database is stuck
	l := NewLedger(w, LedgerOptions{BufferSize: 2, BatchSize: 1, FlushInterval: time.Hour}, quiet)
	start := time.Now()
	for range 10 {
		l.Write(Record{})
	}
	if took := time.Since(start); took > 100*time.Millisecond {
		t.Errorf("10 writes took %v; Write must never block", took)
	}
	// One record is in the stuck write and 2 are in the buffer; the rest are dropped.
	if d := l.Stats().Dropped; d < 7 {
		t.Errorf("dropped = %d, want at least 7", d)
	}
	close(w.gate)
	_ = l.Close(context.Background())
	if s := l.Stats(); s.Written+s.Dropped != 10 {
		t.Errorf("stats = %+v; every record must be either written or counted as dropped", s)
	}
}

func TestLedgerCountsFailedBatches(t *testing.T) {
	w := &fakeWriter{err: errors.New("connection refused")}
	l := NewLedger(w, LedgerOptions{BufferSize: 10, BatchSize: 5, FlushInterval: time.Hour}, quiet)
	for range 3 {
		l.Write(Record{})
	}
	_ = l.Close(context.Background())
	if s := l.Stats(); s.Failed != 3 || s.Written != 0 {
		t.Errorf("stats = %+v, want 3 failed", s)
	}
}

func TestLedgerWriteAfterCloseIsSafe(t *testing.T) {
	l := NewLedger(&fakeWriter{}, LedgerOptions{}, quiet)
	_ = l.Close(context.Background())
	l.Write(Record{}) // must not panic on the closed channel
	if l.Stats().Dropped != 1 {
		t.Errorf("stats = %+v, want the late record counted as dropped", l.Stats())
	}
}
