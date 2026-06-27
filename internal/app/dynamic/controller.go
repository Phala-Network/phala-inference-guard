package dynamic

func New(cfg Config, deps Dependencies) *Controller {
	controller := &Controller{cfg: cfg, deps: deps}
	controller.snapshot.Store(controller.initialSnapshot("startup"))
	return controller
}

func (c *Controller) Start() {
	if c == nil || !c.cfg.Enabled {
		return
	}
	go c.pollLoop()
}
