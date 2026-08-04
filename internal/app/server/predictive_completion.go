package server

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/infra/openai"
)

type predictiveCompletionContextKey struct{}

type predictiveResponseObserver struct {
	reservation    predictiveShadowReservation
	requestStarted time.Time
	streaming      bool
	claimed        atomic.Bool
	counters       *predictiveCompletionObserverCounters
}

func attachPredictiveResponseObserver(r *http.Request, reservation predictiveShadowReservation, requestStarted time.Time, streaming bool, counters *predictiveCompletionObserverCounters) *http.Request {
	if r == nil || reservation == nil || requestStarted.IsZero() {
		return r
	}
	if counters != nil {
		counters.attached.Add(1)
	}
	observer := &predictiveResponseObserver{
		reservation:    reservation,
		requestStarted: requestStarted,
		streaming:      streaming,
		counters:       counters,
	}
	return r.WithContext(context.WithValue(r.Context(), predictiveCompletionContextKey{}, observer))
}

func observePredictiveResponse(response *http.Response) {
	observer := claimPredictiveResponseObserver(response, false)
	if observer == nil {
		return
	}
	response.Body = openai.ObserveCompletionUsageBodyWithTerminalLength(response.Body, false, response.ContentLength, observer.observeUsage, observer.observeTerminal)
}

func newPredictiveStreamingCompletionObserver(response *http.Response) *openai.CompletionUsageObserver {
	observer := claimPredictiveResponseObserver(response, true)
	if observer == nil {
		return nil
	}
	return openai.NewCompletionUsageObserverWithTerminal(true, observer.observeUsage, observer.observeTerminal)
}

func claimPredictiveResponseObserver(response *http.Response, streaming bool) *predictiveResponseObserver {
	if response == nil || response.Body == nil || response.Request == nil || response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil
	}
	observer, ok := response.Request.Context().Value(predictiveCompletionContextKey{}).(*predictiveResponseObserver)
	if !ok || observer == nil || observer.reservation == nil || observer.requestStarted.IsZero() || observer.streaming != streaming {
		return nil
	}
	if !openai.CompletionUsageContentTypeEligible(response.Header.Get("Content-Type"), streaming) || !observer.claimed.CompareAndSwap(false, true) {
		return nil
	}
	if observer.counters != nil {
		observer.counters.claimed.Add(1)
	}
	return observer
}

func (o *predictiveResponseObserver) observeUsage(usage openai.CompletionUsage) {
	if o == nil {
		return
	}
	if o.counters != nil {
		o.counters.usage.Add(1)
	}
	elapsed := time.Since(o.requestStarted)
	if !usage.ObservedAt.IsZero() {
		elapsed = usage.ObservedAt.Sub(o.requestStarted)
	}
	observePredictiveCompletion(o.reservation, predictiveCompletionObservation{
		PromptTokens:          usage.PromptTokens,
		CompletionTokens:      usage.CompletionTokens,
		ObservedAt:            usage.ObservedAt,
		ElapsedSinceRequest:   elapsed,
		BackendMeanITL:        usage.MeanITL,
		BackendGenerationTime: usage.GenerationTime,
	})
}

func (o *predictiveResponseObserver) observeTerminal() {
	if o == nil {
		return
	}
	if o.counters != nil {
		o.counters.terminal.Add(1)
	}
	releaser, ok := o.reservation.(predictiveResourceReleaser)
	if ok {
		releaser.ReleaseResources()
	}
}
