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
}

func attachPredictiveResponseObserver(r *http.Request, reservation predictiveShadowReservation, requestStarted time.Time, streaming bool) *http.Request {
	if r == nil || reservation == nil || requestStarted.IsZero() {
		return r
	}
	observer := &predictiveResponseObserver{
		reservation:    reservation,
		requestStarted: requestStarted,
		streaming:      streaming,
	}
	return r.WithContext(context.WithValue(r.Context(), predictiveCompletionContextKey{}, observer))
}

func observePredictiveResponse(response *http.Response) {
	observer := claimPredictiveResponseObserver(response, false)
	if observer == nil {
		return
	}
	response.Body = openai.ObserveCompletionUsageBody(response.Body, false, observer.observeUsage)
}

func newPredictiveStreamingCompletionObserver(response *http.Response) *openai.CompletionUsageObserver {
	observer := claimPredictiveResponseObserver(response, true)
	if observer == nil {
		return nil
	}
	return openai.NewCompletionUsageObserver(true, observer.observeUsage)
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
	return observer
}

func (o *predictiveResponseObserver) observeUsage(usage openai.CompletionUsage) {
	if o == nil {
		return
	}
	elapsed := time.Since(o.requestStarted)
	if !usage.ObservedAt.IsZero() {
		elapsed = usage.ObservedAt.Sub(o.requestStarted)
	}
	observePredictiveCompletion(o.reservation, predictiveCompletionObservation{
		CompletionTokens:      usage.CompletionTokens,
		ElapsedSinceRequest:   elapsed,
		BackendMeanITL:        usage.MeanITL,
		BackendGenerationTime: usage.GenerationTime,
	})
}
