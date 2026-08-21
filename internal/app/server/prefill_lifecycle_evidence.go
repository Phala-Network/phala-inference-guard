package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	apprequest "github.com/Phala-Network/phala-inference-guard/internal/app/request"
)

type prefillLifecycleOutcome uint8

const (
	prefillLifecycleFirstByteThenTerminal prefillLifecycleOutcome = iota
	prefillLifecycleForwardedTerminalBeforeFirstByte
	prefillLifecyclePreForwardTerminal
	prefillLifecycleOutcomeCount
)

var prefillLifecycleOutcomeLabels = [...]string{
	"first_byte_then_terminal",
	"forwarded_terminal_before_first_byte",
	"pre_forward_terminal",
}

type prefillLifecycleSequenceShape uint8

const (
	prefillLifecycleShapeUnknown prefillLifecycleSequenceShape = iota
	prefillLifecycleShapeSingle
	prefillLifecycleShapeSinglePromptFanout
	prefillLifecycleShapePromptBatch
	prefillLifecycleShapePromptBatchFanout
	prefillLifecycleSequenceShapeCount
)

var prefillLifecycleSequenceShapeLabels = [...]string{
	"unknown",
	"single",
	"single_prompt_fanout",
	"prompt_batch",
	"prompt_batch_fanout",
}

var prefillLifecycleDurationBounds = [...]time.Duration{
	100 * time.Millisecond,
	250 * time.Millisecond,
	500 * time.Millisecond,
	time.Second,
	2 * time.Second,
	5 * time.Second,
	10 * time.Second,
	20 * time.Second,
	30 * time.Second,
	time.Minute,
	2 * time.Minute,
	5 * time.Minute,
	10 * time.Minute,
	30 * time.Minute,
}

var prefillLifecycleDurationBoundLabels = [...]string{
	"0.1",
	"0.25",
	"0.5",
	"1",
	"2",
	"5",
	"10",
	"20",
	"30",
	"60",
	"120",
	"300",
	"600",
	"1800",
}

type prefillLifecycleEvidence struct {
	mu                  sync.Mutex
	outcomes            [prefillLifecycleOutcomeCount][prefillLifecycleSequenceShapeCount]uint64
	durationCount       uint64
	durationNanoseconds uint64
	durationBuckets     [len(prefillLifecycleDurationBounds)]uint64
}

type prefillLifecycleEvidenceSnapshot struct {
	outcomes            [prefillLifecycleOutcomeCount][prefillLifecycleSequenceShapeCount]uint64
	durationCount       uint64
	durationNanoseconds uint64
	durationBuckets     [len(prefillLifecycleDurationBounds)]uint64
}

type prefillLifecycleRequestEvidence struct {
	owner *prefillLifecycleEvidence
	shape prefillLifecycleSequenceShape

	mu          sync.Mutex
	forwarded   bool
	firstByte   bool
	firstByteAt time.Time
	terminal    bool
}

type prefillLifecycleRequestEvidenceContextKey struct{}

func (e *prefillLifecycleEvidence) Begin(
	classification apprequest.Classification,
) *prefillLifecycleRequestEvidence {
	return &prefillLifecycleRequestEvidence{
		owner: e,
		shape: prefillLifecycleSequenceShapeFor(classification),
	}
}

func (e *prefillLifecycleEvidence) record(
	outcome prefillLifecycleOutcome,
	shape prefillLifecycleSequenceShape,
	duration time.Duration,
	hasDuration bool,
) {
	if e == nil {
		return
	}
	if outcome >= prefillLifecycleOutcomeCount {
		outcome = prefillLifecyclePreForwardTerminal
	}
	if shape >= prefillLifecycleSequenceShapeCount {
		shape = prefillLifecycleShapeUnknown
	}
	if duration < 0 {
		duration = 0
	}
	e.mu.Lock()
	e.outcomes[outcome][shape]++
	if hasDuration {
		e.durationCount++
		e.durationNanoseconds += uint64(duration)
		for bucket, upper := range prefillLifecycleDurationBounds {
			if duration <= upper {
				e.durationBuckets[bucket]++
			}
		}
	}
	e.mu.Unlock()
}

func (e *prefillLifecycleEvidence) Snapshot() prefillLifecycleEvidenceSnapshot {
	if e == nil {
		return prefillLifecycleEvidenceSnapshot{}
	}
	e.mu.Lock()
	snapshot := prefillLifecycleEvidenceSnapshot{
		outcomes:            e.outcomes,
		durationCount:       e.durationCount,
		durationNanoseconds: e.durationNanoseconds,
		durationBuckets:     e.durationBuckets,
	}
	e.mu.Unlock()
	return snapshot
}

func writePrefillLifecycleEvidenceMetrics(w io.Writer, snapshot prefillLifecycleEvidenceSnapshot) {
	for outcome, outcomeLabel := range prefillLifecycleOutcomeLabels {
		for shape, shapeLabel := range prefillLifecycleSequenceShapeLabels {
			fmt.Fprintf(
				w,
				"pig_predictive_prefill_lifecycle_total{outcome=%q,sequence_shape=%q} %d\n",
				outcomeLabel,
				shapeLabel,
				snapshot.outcomes[outcome][shape],
			)
		}
	}
	fmt.Fprintf(w, "pig_predictive_prefill_first_byte_to_terminal_seconds_count %d\n", snapshot.durationCount)
	fmt.Fprintf(
		w,
		"pig_predictive_prefill_first_byte_to_terminal_seconds_sum %.6f\n",
		float64(snapshot.durationNanoseconds)/float64(time.Second),
	)
	for bucket, label := range prefillLifecycleDurationBoundLabels {
		fmt.Fprintf(
			w,
			"pig_predictive_prefill_first_byte_to_terminal_seconds_bucket{le=%q} %d\n",
			label,
			snapshot.durationBuckets[bucket],
		)
	}
	fmt.Fprintf(
		w,
		"pig_predictive_prefill_first_byte_to_terminal_seconds_bucket{le=%q} %d\n",
		"+Inf",
		snapshot.durationCount,
	)
}

func (r *prefillLifecycleRequestEvidence) MarkForwarded() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.terminal || r.forwarded {
		return false
	}
	r.forwarded = true
	return true
}

func (r *prefillLifecycleRequestEvidence) MarkFirstByte(now time.Time) bool {
	if r == nil || now.IsZero() {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.terminal || !r.forwarded || r.firstByte {
		return false
	}
	r.firstByte = true
	r.firstByteAt = now
	return true
}

func (r *prefillLifecycleRequestEvidence) Terminate(now time.Time) bool {
	if r == nil || now.IsZero() {
		return false
	}
	r.mu.Lock()
	if r.terminal {
		r.mu.Unlock()
		return false
	}
	r.terminal = true
	outcome := prefillLifecyclePreForwardTerminal
	duration := time.Duration(0)
	hasDuration := false
	if r.forwarded {
		outcome = prefillLifecycleForwardedTerminalBeforeFirstByte
		if r.firstByte {
			outcome = prefillLifecycleFirstByteThenTerminal
			duration = now.Sub(r.firstByteAt)
			hasDuration = true
		}
	}
	owner := r.owner
	shape := r.shape
	r.mu.Unlock()
	owner.record(outcome, shape, duration, hasDuration)
	return true
}

func prefillLifecycleSequenceShapeFor(
	classification apprequest.Classification,
) prefillLifecycleSequenceShape {
	estimate := classification.Cost.Estimate
	basePrompts := estimate.BasePromptCount
	decodeSequences := estimate.DecodeSequences
	if basePrompts <= 0 || decodeSequences <= 0 || basePrompts > decodeSequences ||
		decodeSequences%basePrompts != 0 {
		return prefillLifecycleShapeUnknown
	}
	switch {
	case basePrompts == 1 && decodeSequences == 1:
		return prefillLifecycleShapeSingle
	case basePrompts == 1:
		return prefillLifecycleShapeSinglePromptFanout
	case basePrompts == decodeSequences:
		return prefillLifecycleShapePromptBatch
	default:
		return prefillLifecycleShapePromptBatchFanout
	}
}

func attachPrefillLifecycleRequestEvidence(
	ctx context.Context,
	evidence *prefillLifecycleRequestEvidence,
) context.Context {
	if evidence == nil {
		return ctx
	}
	return context.WithValue(ctx, prefillLifecycleRequestEvidenceContextKey{}, evidence)
}

func prefillLifecycleRequestEvidenceFrom(response *http.Response) *prefillLifecycleRequestEvidence {
	if response == nil || response.Request == nil {
		return nil
	}
	evidence, _ := response.Request.Context().Value(prefillLifecycleRequestEvidenceContextKey{}).(*prefillLifecycleRequestEvidence)
	return evidence
}
