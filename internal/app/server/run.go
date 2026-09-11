package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func Run() error {
	configureRuntimeLogging()
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runServer(ctx, cfg)
}

func runServer(ctx context.Context, cfg config) (runErr error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	srv, err := newProxyServerContext(ctx, cfg)
	if err != nil {
		if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			return nil
		}
		return err
	}
	defer func() { runErr = errors.Join(runErr, srv.Close()) }()
	if ctx.Err() != nil {
		return nil
	}
	log.Printf("level=info component=runtime event=startup version=%s listen=%s upstream=%s metrics=%s observer=%s freshness=%s predictive_admission=%s tps_reference=%.6f window_concurrency=%d configured_running_limit=%d status_interval=%s log_level=%s upstream_error_classification=%t",
		version, cfg.Listen, cfg.Upstream, cfg.PredictiveMetricsURL,
		cfg.PredictiveObservationPollInterval, cfg.PredictiveMaximumMetricsAge,
		cfg.PredictiveAdmissionMode, cfg.PredictiveTPSReference,
		cfg.PredictiveWindowConcurrency, cfg.PredictiveRunningLimit, cfg.StatusLogInterval,
		cfg.LogLevel, cfg.UpstreamErrorClassificationEnabled)
	log.Print(srv.statusLogLine())
	if cfg.StatusLogInterval > 0 {
		go srv.statusLogLoop(ctx)
	}
	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv,
		ReadHeaderTimeout: 30 * time.Second,
	}
	return serveHTTPContext(ctx, httpSrv)
}

// A signal must cancel startup immediately and must not leave a ready server
// ignoring SIGTERM. Bound serving shutdown so hung upstream streams cannot
// prevent container termination; admission is closed after handlers finish.
func serveHTTPContext(ctx context.Context, srv *http.Server) error {
	finished := make(chan struct{})
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := srv.Shutdown(shutdownCtx); err != nil {
				_ = srv.Close()
			}
		case <-finished:
		}
	}()
	err := srv.ListenAndServe()
	close(finished)
	<-shutdownDone
	if ctx.Err() != nil && errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
