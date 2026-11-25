package usage

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// BatchWriter stores a batch of records, for example in Postgres.
type BatchWriter interface {
	WriteBatch(ctx context.Context, recs []Record) error
}

// LedgerOptions tune the background writer.
type LedgerOptions struct {
	BufferSize    int           // records waiting to be written; when full, new ones are dropped
	BatchSize     int           // records per database write
	FlushInterval time.Duration // longest wait before a partial batch is written
}

// LedgerStats are counters for monitoring.
type LedgerStats struct {
	Written int64 // records stored
	Dropped int64 // records lost because the buffer was full
	Failed  int64 // records lost because the database rejected the batch
}

// Ledger writes usage records in the background. Write never blocks a
// request: it puts the record in a buffered channel, and one worker writes
// the records in batches. If the database is slow or down and the buffer
// fills up, new records are dropped and counted. Serving requests matters
// more than the ledger, and every record is still in the JSON log.
type Ledger struct {
	w    BatchWriter
	opts LedgerOptions
	log  *slog.Logger

	mu     sync.RWMutex // guards ch against a Write after Close
	closed bool
	ch     chan Record
	done   chan struct{}

	written, dropped, failed atomic.Int64
}

// NewLedger starts the background writer.
func NewLedger(w BatchWriter, opts LedgerOptions, log *slog.Logger) *Ledger {
	opts.BufferSize = max(opts.BufferSize, 1)
	opts.BatchSize = max(opts.BatchSize, 1)
	if opts.FlushInterval <= 0 {
		opts.FlushInterval = time.Second
	}
	l := &Ledger{w: w, opts: opts, log: log, ch: make(chan Record, opts.BufferSize), done: make(chan struct{})}
	go l.run()
	return l
}

// Write queues a record. It never blocks.
func (l *Ledger) Write(r Record) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.closed {
		l.dropped.Add(1)
		return
	}
	select {
	case l.ch <- r:
	default:
		l.dropped.Add(1) // buffer full: drop rather than slow the request down
	}
}

// Close stops taking records and waits until the queued ones are written,
// or until ctx ends.
func (l *Ledger) Close(ctx context.Context) error {
	l.mu.Lock()
	if !l.closed {
		l.closed = true
		close(l.ch)
	}
	l.mu.Unlock()
	select {
	case <-l.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stats returns the counters.
func (l *Ledger) Stats() LedgerStats {
	return LedgerStats{Written: l.written.Load(), Dropped: l.dropped.Load(), Failed: l.failed.Load()}
}

func (l *Ledger) run() {
	defer close(l.done)
	ticker := time.NewTicker(l.opts.FlushInterval)
	defer ticker.Stop()
	batch := make([]Record, 0, l.opts.BatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := l.w.WriteBatch(ctx, batch)
		cancel()
		if err != nil {
			l.failed.Add(int64(len(batch)))
			l.log.Error("usage ledger write failed; records dropped", "records", len(batch), "error", err.Error())
		} else {
			l.written.Add(int64(len(batch)))
		}
		batch = batch[:0]
	}
	for {
		select {
		case r, ok := <-l.ch:
			if !ok {
				flush() // Close was called: write what is left
				return
			}
			batch = append(batch, r)
			if len(batch) >= l.opts.BatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}
