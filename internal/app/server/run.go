package server

import (
	"errors"
	"log"
	"net/http"
	"time"
)

func Run() (runErr error) {
	configureRuntimeLogging()
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	srv, err := newProxyServer(cfg)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, srv.Close()) }()
	log.Printf("level=info component=runtime event=startup version=%s listen=%s upstream=%s metrics=%s observer=%s freshness=%s predictive_admission=%s tps_reference=%.6f window_concurrency=%d configured_running_limit=%d status_interval=%s log_level=%s upstream_error_classification=%t",
		version, cfg.Listen, cfg.Upstream, cfg.PredictiveMetricsURL,
		cfg.PredictiveObservationPollInterval, cfg.PredictiveMaximumMetricsAge,
		cfg.PredictiveAdmissionMode, cfg.PredictiveTPSReference,
		cfg.PredictiveWindowConcurrency, cfg.PredictiveRunningLimit, cfg.StatusLogInterval,
		cfg.LogLevel, cfg.UpstreamErrorClassificationEnabled)
	log.Print(srv.statusLogLine())
	if cfg.StatusLogInterval > 0 {
		go srv.statusLogLoop()
	}
	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv,
		ReadHeaderTimeout: 30 * time.Second,
	}
	return httpSrv.ListenAndServe()
}
