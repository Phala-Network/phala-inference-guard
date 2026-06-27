package backend

import (
	"context"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/infra/http"
	runtimebackend "github.com/Phala-Network/phala-inference-guard/internal/runtime/backend"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

type Config struct {
	Name       string
	Upstream   string
	MetricsURL string
}

type Proxy struct {
	cfg       Config
	target    *url.URL
	proxy     *httputil.ReverseProxy
	transport http.RoundTripper
	inflight  atomic.Int64
	accepted  atomic.Uint64
	completed atomic.Uint64
	proxyErrs atomic.Uint64
	copyErrs  atomic.Uint64
	failed    atomic.Uint64
	status    atomic.Value
}

type Stats struct {
	Inflight  int64
	Accepted  uint64
	Completed uint64
	Failed    uint64
	ProxyErrs uint64
	CopyErrs  uint64
}

func Build(configs []Config) ([]*Proxy, *url.URL, *httputil.ReverseProxy, error) {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          512,
		MaxIdleConnsPerHost:   512,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	bufferPool := httpx.NewBufferPool(32 * 1024)
	backends := make([]*Proxy, 0, len(configs))
	var firstTarget *url.URL
	var firstProxy *httputil.ReverseProxy
	for _, cfg := range configs {
		target, err := url.Parse(cfg.Upstream)
		if err != nil {
			return nil, nil, nil, err
		}
		rp := httputil.NewSingleHostReverseProxy(target)
		backend := &Proxy{cfg: cfg, target: target, proxy: rp, transport: transport}
		rp.Transport = transport
		rp.BufferPool = bufferPool
		rp.FlushInterval = -1
		backend.status.Store(runtimebackend.Runtime{Name: cfg.Name})
		backends = append(backends, backend)
		if firstTarget == nil {
			firstTarget = target
			firstProxy = rp
		}
	}
	return backends, firstTarget, firstProxy, nil
}

func (b *Proxy) Name() string {
	return b.cfg.Name
}

func (b *Proxy) Upstream() string {
	return b.cfg.Upstream
}

func (b *Proxy) MetricsURL() string {
	return b.cfg.MetricsURL
}

func (b *Proxy) Inflight() int64 {
	return b.inflight.Load()
}

func (b *Proxy) Stats() Stats {
	return Stats{
		Inflight:  b.inflight.Load(),
		Accepted:  b.accepted.Load(),
		Completed: b.completed.Load(),
		Failed:    b.failed.Load(),
		ProxyErrs: b.proxyErrs.Load(),
		CopyErrs:  b.copyErrs.Load(),
	}
}

func (b *Proxy) SetHandlers(modify func(*http.Response) error, errorHandler func(http.ResponseWriter, *http.Request, error)) {
	b.proxy.ModifyResponse = modify
	b.proxy.ErrorHandler = errorHandler
}

func (b *Proxy) Begin() func() {
	b.inflight.Add(1)
	b.accepted.Add(1)
	return func() {
		b.inflight.Add(-1)
		b.completed.Add(1)
	}
}

func (b *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.proxy.ServeHTTP(w, r)
}

func (b *Proxy) RoundTrip(r *http.Request) (*http.Response, error) {
	return b.transport.RoundTrip(r)
}

func (b *Proxy) NewUpstreamRequest(ctx context.Context, r *http.Request) *http.Request {
	out := r.Clone(ctx)
	out.URL.Scheme = b.target.Scheme
	out.URL.Host = b.target.Host
	out.URL.Path = httpx.JoinURLPath(b.target, r.URL)
	out.URL.RawPath = ""
	out.URL.RawQuery = r.URL.RawQuery
	out.RequestURI = ""
	out.Host = b.target.Host
	out.Header = httpx.CloneHeader(r.Header)
	httpx.RemoveHopByHopHeaders(out.Header)
	httpx.AddXForwardedFor(out, r)
	out.Header.Set("X-PIG-Backend", b.cfg.Name)
	return out
}

func (b *Proxy) Status() runtimebackend.Runtime {
	raw := b.status.Load()
	status, ok := raw.(runtimebackend.Runtime)
	if !ok {
		return runtimebackend.Runtime{Name: b.cfg.Name, Failed: true, Error: "status_unavailable"}
	}
	return status
}

func (b *Proxy) StoreStatus(status runtimebackend.Runtime) {
	if status.Name == "" {
		status.Name = b.cfg.Name
	}
	b.status.Store(status)
}

func (b *Proxy) UpdateStatusFromSample(sample telemetry.Sample) runtimebackend.Runtime {
	now := time.Now()
	previous := b.Status()
	status := runtimebackend.FromSample(b.cfg.Name, sample, previous, now)
	b.StoreStatus(status)
	return status
}

func (b *Proxy) ObserveMetricsFailure() {
	b.failed.Add(1)
}

func (b *Proxy) ObserveProxyError() {
	b.proxyErrs.Add(1)
}

func (b *Proxy) ObserveCopyError() {
	b.copyErrs.Add(1)
}
