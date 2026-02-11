package ingestion

import (
	"context"
	"time"
)

// DebouncerConfig configures the debouncing window.
type DebouncerConfig struct {
	Window time.Duration
}

// Debouncer coalesces events over a configurable time window, merging
// same-keyed events (last-write-wins) and preserving non-mergeable events.
type Debouncer struct {
	cfg DebouncerConfig
}

// NewDebouncer creates a Debouncer with the given config.
func NewDebouncer(cfg DebouncerConfig) *Debouncer {
	return &Debouncer{cfg: cfg}
}

// Run reads events from inCh, coalesces them within the debounce window, and
// emits batches on the returned channel. The output channel is closed when
// inCh is closed or ctx is cancelled.
func (d *Debouncer) Run(ctx context.Context, inCh <-chan Event) <-chan []Event {
	outCh := make(chan []Event, 1)
	go d.loop(ctx, inCh, outCh)
	return outCh
}

func (d *Debouncer) loop(ctx context.Context, inCh <-chan Event, outCh chan<- []Event) {
	defer close(outCh)

	merged := make(map[string]Event)
	var nonMergeable []Event
	var timer *time.Timer
	var timerCh <-chan time.Time

	emit := func(batch []Event) {
		select {
		case outCh <- batch:
		case <-ctx.Done():
		}
	}

	// finalFlush sends remaining events without a ctx guard, since it is
	// called after ctx cancellation or input close.
	finalFlush := func() {
		batch := d.buildBatch(merged, nonMergeable)
		if len(batch) == 0 {
			return
		}
		outCh <- batch
		merged = make(map[string]Event)
		nonMergeable = nil
	}

	flush := func() {
		batch := d.buildBatch(merged, nonMergeable)
		if len(batch) == 0 {
			return
		}
		emit(batch)
		merged = make(map[string]Event)
		nonMergeable = nil
	}

	resetTimer := func() {
		if timer != nil {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
		timer = time.NewTimer(d.cfg.Window)
		timerCh = timer.C
	}

	for {
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			finalFlush()
			return
		case ev, ok := <-inCh:
			if !ok {
				if timer != nil {
					timer.Stop()
				}
				finalFlush()
				return
			}
			if ev.isMergeable() {
				merged[ev.mergeKey()] = ev
			} else {
				nonMergeable = append(nonMergeable, ev)
			}
			if timerCh == nil {
				resetTimer()
			}
		case <-timerCh:
			flush()
			timerCh = nil
			timer = nil
		}
	}
}

func (d *Debouncer) buildBatch(merged map[string]Event, nonMergeable []Event) []Event {
	batch := make([]Event, 0, len(merged)+len(nonMergeable))
	for _, ev := range merged {
		batch = append(batch, ev)
	}
	batch = append(batch, nonMergeable...)
	return batch
}
