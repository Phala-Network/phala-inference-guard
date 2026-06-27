package request

import (
	"bytes"
	"io"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	requestclass "github.com/Phala-Network/phala-inference-guard/internal/domain/request"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

type PriorityConfig struct {
	Enabled             bool
	Mode                string
	Strategy            string
	Field               string
	PremiumValue        int
	BasicValue          int
	BodyBytes           int64
	BufferBytes         int64
	StreamBufferBytes   int
	Limit               int
	FailOpen            bool
	StripEmptyToolCalls bool
	CompatBodyBytes     int64
	CompatFailOpen      bool
}

type PriorityStats struct {
	Enabled            bool
	BodyBytes          int64
	BufferBytes        int64
	StreamBufferBytes  int
	Limit              int
	Inflight           int64
	Rewritten          uint64
	Skipped            uint64
	Failed             uint64
	DurationCount      uint64
	DurationSeconds    float64
	DurationBuckets    []telemetry.HistogramBucketSample
	DurationMaxSeconds float64
}

type PriorityInjector struct {
	cfg             PriorityConfig
	tokens          chan struct{}
	inflight        atomic.Int64
	rewritten       atomic.Uint64
	skipped         atomic.Uint64
	failed          atomic.Uint64
	durationN       atomic.Uint64
	durationNS      atomic.Int64
	durationBuckets []atomic.Uint64
	durationMaxNS   atomic.Uint64
}

var priorityRewriteDurationBucketsSeconds = []float64{0.0005, 0.001, 0.002, 0.005, 0.01, 0.02, 0.05, 0.1, 0.25, 0.5, 1, 2, 5}

const minPriorityStreamBufferBytes = 4 * 1024

func NewPriorityInjector(cfg PriorityConfig) *PriorityInjector {
	injector := &PriorityInjector{
		cfg:             cfg,
		durationBuckets: make([]atomic.Uint64, len(priorityRewriteDurationBucketsSeconds)),
	}
	if cfg.Limit > 0 {
		injector.tokens = make(chan struct{}, cfg.Limit)
	}
	return injector
}

func (p *PriorityInjector) Enabled() bool {
	return p != nil && p.cfg.Enabled
}

func (p *PriorityInjector) Inject(r *http.Request, tier requestclass.Tier) bool {
	if p == nil {
		return true
	}
	injectPriority := p.cfg.Enabled && requestclass.ShouldInjectBackendPriority(p.cfg.Mode, tier)
	if p.cfg.Enabled && !injectPriority {
		p.skipped.Add(1)
	}
	stripEmptyToolCalls := p.cfg.StripEmptyToolCalls
	if !injectPriority && !stripEmptyToolCalls {
		return true
	}
	if r == nil || r.Body == nil {
		if injectPriority {
			return p.skipOrFail()
		}
		return true
	}
	if contentType := r.Header.Get("Content-Type"); contentType != "" && !containsASCIIInsensitive(contentType, "json") {
		if injectPriority {
			return p.skipOrFail()
		}
		return true
	}

	priorityEligible := injectPriority && eligibleBodyLength(r.ContentLength, p.cfg.BodyBytes)
	compatEligible := stripEmptyToolCalls && eligibleBodyLength(r.ContentLength, p.cfg.CompatBodyBytes)
	if injectPriority && !priorityEligible {
		p.skipped.Add(1)
		if !p.cfg.FailOpen {
			return false
		}
	}
	if !priorityEligible && !compatEligible {
		return true
	}
	if !p.acquire() {
		if priorityEligible {
			return p.skipOrFail()
		}
		return p.cfg.CompatFailOpen
	}

	start := time.Now()
	originalBody := r.Body
	value := requestclass.BackendPriorityValue(tier, p.cfg.PremiumValue, p.cfg.BasicValue)
	options := requestclass.JSONRewriteOptions{
		InjectPriority:      priorityEligible,
		PriorityStrategy:    p.cfg.Strategy,
		PriorityField:       p.cfg.Field,
		PriorityValue:       value,
		StripEmptyToolCalls: compatEligible,
	}
	rewritten, contentLength, err := p.rewriteBody(originalBody, options, r.ContentLength)
	if err != nil {
		failOpen := true
		if priorityEligible {
			p.failed.Add(1)
			failOpen = failOpen && p.cfg.FailOpen
		}
		if compatEligible {
			failOpen = failOpen && p.cfg.CompatFailOpen
		}
		if failOpen && rewritten != nil {
			p.setRequestBody(r, rewritten, contentLength, start, priorityEligible)
			return true
		}
		if priorityEligible {
			p.observeDuration(time.Since(start))
		}
		p.release()
		return false
	}

	p.setRequestBody(r, rewritten, contentLength, start, priorityEligible)
	if priorityEligible {
		p.rewritten.Add(1)
	}
	return true
}

func (p *PriorityInjector) setRequestBody(r *http.Request, body io.ReadCloser, contentLength int64, start time.Time, observe bool) {
	var observeDuration func(time.Duration)
	if observe {
		observeDuration = p.observeDuration
	}
	r.Body = &releaseOnDoneReadCloser{
		ReadCloser: body,
		start:      start,
		release:    p.release,
		observe:    observeDuration,
	}
	if contentLength >= 0 {
		r.ContentLength = contentLength
		r.Header.Set("Content-Length", strconv.FormatInt(contentLength, 10))
	} else {
		r.ContentLength = -1
		r.Header.Del("Content-Length")
	}
}

func (p *PriorityInjector) rewriteBody(body io.ReadCloser, options requestclass.JSONRewriteOptions, contentLength int64) (io.ReadCloser, int64, error) {
	if p.shouldBufferRewrite(options, contentLength) {
		return p.rewriteBufferedBody(body, options)
	}
	streamBufferBytes := p.streamBufferBytes(contentLength)
	if options.InjectPriority && !options.StripEmptyToolCalls && p.cfg.Strategy == requestclass.PriorityRewriteStrategyAppendLast {
		rewritten, err := requestclass.NewAppendLastJSONPriorityRewriteSize(body, p.cfg.Field, options.PriorityValue, streamBufferBytes)
		return rewritten, -1, err
	}
	rewritten, err := requestclass.NewStreamingJSONBodyRewriteSize(body, options, streamBufferBytes)
	return rewritten, -1, err
}

func (p *PriorityInjector) shouldBuffer(contentLength int64) bool {
	return p.cfg.BufferBytes > 0 && contentLength >= 0 && contentLength <= p.cfg.BufferBytes
}

func (p *PriorityInjector) shouldBufferRewrite(options requestclass.JSONRewriteOptions, contentLength int64) bool {
	if options.InjectPriority && p.cfg.Strategy != requestclass.PriorityRewriteStrategyAppendLast && p.shouldBuffer(contentLength) {
		return true
	}
	return contentLength >= 0 && ((options.InjectPriority && !p.cfg.FailOpen) || (options.StripEmptyToolCalls && !p.cfg.CompatFailOpen))
}

func (p *PriorityInjector) streamBufferBytes(contentLength int64) int {
	configured := p.cfg.StreamBufferBytes
	if contentLength < 0 || configured <= 0 || contentLength >= int64(configured) {
		return configured
	}
	return bucketedPriorityStreamBufferBytes(contentLength, configured)
}

func bucketedPriorityStreamBufferBytes(contentLength int64, configured int) int {
	if contentLength <= minPriorityStreamBufferBytes {
		return minPriorityStreamBufferBytes
	}
	size := minPriorityStreamBufferBytes
	for int64(size) < contentLength {
		if size >= configured {
			return configured
		}
		next := size * 2
		if next <= size || next > configured {
			return configured
		}
		size = next
	}
	return size
}

func (p *PriorityInjector) rewriteBufferedBody(body io.ReadCloser, options requestclass.JSONRewriteOptions) (io.ReadCloser, int64, error) {
	original, readErr := io.ReadAll(body)
	_ = body.Close()
	if readErr != nil {
		return nil, -1, readErr
	}
	bufferSize := p.streamBufferBytes(int64(len(original)))
	rewritten, err := requestclass.RewriteJSONBodySize(original, options, bufferSize)
	if err != nil {
		return io.NopCloser(bytes.NewReader(original)), int64(len(original)), err
	}
	return io.NopCloser(bytes.NewReader(rewritten)), int64(len(rewritten)), nil
}

func eligibleBodyLength(contentLength int64, maxBytes int64) bool {
	return contentLength >= 0 && contentLength <= maxBytes
}

type releaseOnDoneReadCloser struct {
	io.ReadCloser
	start   time.Time
	release func()
	observe func(time.Duration)
	once    sync.Once
}

func (r *releaseOnDoneReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if err != nil {
		r.finish()
	}
	return n, err
}

func (r *releaseOnDoneReadCloser) Close() error {
	r.finish()
	return r.ReadCloser.Close()
}

func (r *releaseOnDoneReadCloser) finish() {
	r.once.Do(func() {
		if r.observe != nil {
			r.observe(time.Since(r.start))
		}
		if r.release != nil {
			r.release()
		}
	})
}

func (p *PriorityInjector) Stats() PriorityStats {
	if p == nil {
		return PriorityStats{}
	}
	buckets := make([]telemetry.HistogramBucketSample, 0, len(priorityRewriteDurationBucketsSeconds))
	for index, upper := range priorityRewriteDurationBucketsSeconds {
		count := uint64(0)
		if index < len(p.durationBuckets) {
			count = p.durationBuckets[index].Load()
		}
		buckets = append(buckets, telemetry.HistogramBucketSample{
			UpperBound: upper,
			Count:      count,
		})
	}
	return PriorityStats{
		Enabled:            p.cfg.Enabled,
		BodyBytes:          p.cfg.BodyBytes,
		BufferBytes:        p.cfg.BufferBytes,
		StreamBufferBytes:  p.cfg.StreamBufferBytes,
		Limit:              p.cfg.Limit,
		Inflight:           p.inflight.Load(),
		Rewritten:          p.rewritten.Load(),
		Skipped:            p.skipped.Load(),
		Failed:             p.failed.Load(),
		DurationCount:      p.durationN.Load(),
		DurationSeconds:    float64(p.durationNS.Load()) / float64(time.Second),
		DurationBuckets:    buckets,
		DurationMaxSeconds: float64(p.durationMaxNS.Load()) / float64(time.Second),
	}
}

func (p *PriorityInjector) acquire() bool {
	if p.tokens == nil {
		return true
	}
	select {
	case p.tokens <- struct{}{}:
		p.inflight.Add(1)
		return true
	default:
		return false
	}
}

func (p *PriorityInjector) release() {
	if p.tokens == nil {
		return
	}
	select {
	case <-p.tokens:
		p.inflight.Add(-1)
	default:
	}
}

func (p *PriorityInjector) skipOrFail() bool {
	p.skipped.Add(1)
	return p.cfg.FailOpen
}

func (p *PriorityInjector) observeDuration(duration time.Duration) {
	if duration < 0 {
		duration = 0
	}
	ns := duration.Nanoseconds()
	p.durationN.Add(1)
	p.durationNS.Add(ns)
	seconds := duration.Seconds()
	for index, upper := range priorityRewriteDurationBucketsSeconds {
		if seconds <= upper && index < len(p.durationBuckets) {
			p.durationBuckets[index].Add(1)
		}
	}
	p.observeMaxDuration(uint64(ns))
}

func (p *PriorityInjector) observeMaxDuration(ns uint64) {
	for {
		current := p.durationMaxNS.Load()
		if ns <= current {
			return
		}
		if p.durationMaxNS.CompareAndSwap(current, ns) {
			return
		}
	}
}

func containsASCIIInsensitive(value, needle string) bool {
	if needle == "" {
		return true
	}
	if len(needle) > len(value) {
		return false
	}
	for i := 0; i <= len(value)-len(needle); i++ {
		matched := true
		for j := 0; j < len(needle); j++ {
			if asciiLower(value[i+j]) != asciiLower(needle[j]) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func asciiLower(value byte) byte {
	if value >= 'A' && value <= 'Z' {
		return value + ('a' - 'A')
	}
	return value
}
