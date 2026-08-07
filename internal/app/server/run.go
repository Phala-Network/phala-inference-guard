package server

import (
	"errors"
	"log"
	"net/http"
	"time"
)

func Run() (runErr error) {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	srv, err := newProxyServer(cfg)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, srv.Close()) }()
	log.Printf("phala-inference-guard %s listen=%s upstream=%s metrics=%s observer=%s freshness=%s predictive_admission=%s target_tps=%.1f/%.1f upstream_error_classification=%t",
		version, cfg.Listen, cfg.Upstream, cfg.PredictiveMetricsURL,
		cfg.PredictiveObservationPollInterval, cfg.PredictiveMaximumMetricsAge,
		cfg.PredictiveAdmissionMode, cfg.PredictiveTPSFloor, cfg.PredictiveTPSTarget,
		cfg.UpstreamErrorClassificationEnabled)
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
