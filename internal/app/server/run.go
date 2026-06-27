package server

import (
	"log"
	"net/http"
	"time"
)

func Run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	srv, err := newProxyServer(cfg)
	if err != nil {
		return err
	}
	log.Printf("phala-inference-guard %s listen=%s upstream=%s backends=%d dynamic=%t/%t metrics=%d queue=%s poll=%s status_log=%s target_tps=%.1f/%.1f capacity_learning=cap learn=%t ttft=%t sse_keepalive=%t sse_early_bridge=%t global_cap=%d",
		version, cfg.Listen, cfg.Upstream, len(cfg.Backends), cfg.DynamicEnabled, cfg.DynamicEnforce, len(cfg.DynamicMetricsURLs), cfg.QoSQueueWait, cfg.QoSQueuePoll, cfg.StatusLogInterval, cfg.DynamicUserTPSRed, cfg.DynamicUserTPSYellow, cfg.DynamicUserTPSCapacityLearn, cfg.DynamicTTFTEnabled, cfg.SSEKeepAliveEnabled, cfg.SSEEarlyBridgeEnabled, cfg.GlobalLimit)
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
