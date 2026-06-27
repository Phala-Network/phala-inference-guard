package decision

type Limits struct {
	HardGlobal   int
	BaseGlobal   int
	State        int
	QOS          int
	Throughput   int
	Capacity     int
	TTFT         int
	Pressure     int
	Prefill      int
	Availability int
	Final        int
}

type Decision struct {
	State         string
	YellowReasons []string
	RedReasons    []string
	Limits        Limits
}

func New(state string, yellowReasons, redReasons []string, limits Limits) Decision {
	return Decision{
		State:         state,
		YellowReasons: cloneReasons(yellowReasons),
		RedReasons:    cloneReasons(redReasons),
		Limits:        limits,
	}
}

func (d Decision) Valid() bool {
	return d.State != "" || len(d.YellowReasons) > 0 || len(d.RedReasons) > 0 || d.Limits.Final > 0
}

func (d Decision) StateOr(fallback string) string {
	if d.State != "" {
		return d.State
	}
	return fallback
}

func (d Decision) PrimaryReasons() []string {
	if len(d.RedReasons) > 0 {
		return cloneReasons(d.RedReasons)
	}
	return cloneReasons(d.YellowReasons)
}

type Builder struct {
	yellowReasons []string
	redReasons    []string
}

func NewBuilder(yellowReasons, redReasons []string) Builder {
	return Builder{
		yellowReasons: cloneReasons(yellowReasons),
		redReasons:    cloneReasons(redReasons),
	}
}

func NewBuilderFromSignal(result SignalResult) Builder {
	return NewBuilder(result.YellowReasons, result.RedReasons)
}

func (b *Builder) AddYellow(reason string) {
	if reason != "" {
		b.yellowReasons = append(b.yellowReasons, reason)
	}
}

func (b *Builder) AddYellowOnce(reason string) {
	b.yellowReasons = AddReason(b.yellowReasons, reason)
}

func (b *Builder) AddRed(reason string) {
	if reason != "" {
		b.redReasons = append(b.redReasons, reason)
	}
}

func (b *Builder) AddRedOnce(reason string) {
	b.redReasons = AddReason(b.redReasons, reason)
}

func (b Builder) State() string {
	return StateFromReasons(b.yellowReasons, b.redReasons)
}

func (b Builder) YellowReasons() []string {
	return cloneReasons(b.yellowReasons)
}

func (b Builder) RedReasons() []string {
	return cloneReasons(b.redReasons)
}

func (b Builder) Build(limits Limits) Decision {
	return New(b.State(), b.yellowReasons, b.redReasons, limits)
}

func StateFromReasons(yellowReasons, redReasons []string) string {
	if len(redReasons) > 0 {
		return "red"
	}
	if len(yellowReasons) > 0 {
		return "yellow"
	}
	return "green"
}

func ValidState(state string) bool {
	switch state {
	case "green", "yellow", "red":
		return true
	default:
		return false
	}
}

func ContainsReason(reasons []string, target string) bool {
	for _, reason := range reasons {
		if reason == target {
			return true
		}
	}
	return false
}

func AddReason(reasons []string, reason string) []string {
	if reason == "" || ContainsReason(reasons, reason) {
		return reasons
	}
	return append(reasons, reason)
}

func cloneReasons(reasons []string) []string {
	if len(reasons) == 0 {
		return nil
	}
	cloned := make([]string, len(reasons))
	copy(cloned, reasons)
	return cloned
}
