package dynamic

func New(cfg Config, deps Dependencies) *Controller {
	cfg.MetricsURLs = append([]string(nil), cfg.MetricsURLs...)
	controller := &Controller{cfg: cfg, deps: deps}
	controller.admission.Store(&cfg)
	controller.configGeneration.Store(1)
	controller.snapshot.Store(controller.initialSnapshot("startup"))
	return controller
}

// AdmissionConfig returns an immutable copy of the currently active policy.
func (c *Controller) AdmissionConfig() Config {
	if c == nil || c.admission.Load() == nil {
		return Config{}
	}
	cfg := *c.admission.Load()
	cfg.MetricsURLs = append([]string(nil), cfg.MetricsURLs...)
	return cfg
}

// SetAdmissionConfig atomically replaces the process-local admission policy.
func (c *Controller) SetAdmissionConfig(cfg Config) {
	if c != nil {
		cfg.MetricsURLs = append([]string(nil), cfg.MetricsURLs...)
		c.publishMu.Lock()
		defer c.publishMu.Unlock()
		c.admission.Store(&cfg)
		c.configGeneration.Add(1)
		c.pressureCap.Reset()
		c.lastMetricsSnapshot.Store(c.initialSnapshot("policy_reset"))
		c.staticMetricsState.Store(staticMetricState{})
		// Reset all learned state and publish a conservative snapshot for every
		// policy revision. Enabled enforcement resumes only after a fresh poll.
		c.snapshot.Store(c.initialSnapshot("runtime_config"))
		c.notify()
	}
}

func (c *Controller) HasMetricsSource() bool {
	if c == nil {
		return false
	}
	cfg := c.AdmissionConfig()
	if cfg.BackendRouting {
		for _, backend := range c.deps.Backends {
			if backend.MetricsURL() != "" {
				return true
			}
		}
		return false
	}
	return len(cfg.MetricsURLs) > 0
}

func (c *Controller) Start() {
	if c == nil || (!c.cfg.BackendRouting && len(c.cfg.MetricsURLs) == 0) {
		return
	}
	go c.pollLoop()
}
