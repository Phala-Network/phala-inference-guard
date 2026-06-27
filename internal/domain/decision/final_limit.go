package decision

type LimitComponent struct {
	Reason string
	Limit  int
}

type FinalLimit struct {
	Limit  int
	Reason string
}

func EnforceFinalLimit(overrideReason string, components ...LimitComponent) FinalLimit {
	return EnforceFinalLimitComponents(overrideReason, components)
}

func EnforceFinalLimitComponents(overrideReason string, components []LimitComponent) FinalLimit {
	if overrideReason != "" {
		return FinalLimit{Limit: 0, Reason: overrideReason}
	}
	if len(components) == 0 {
		return FinalLimit{Limit: 0, Reason: "none"}
	}
	final := FinalLimit{
		Limit:  components[0].Limit,
		Reason: limitComponentReason(components[0].Reason),
	}
	if final.Limit <= 0 {
		return FinalLimit{Limit: 0, Reason: final.Reason}
	}
	for _, component := range components[1:] {
		reason := limitComponentReason(component.Reason)
		if component.Limit <= 0 {
			return FinalLimit{Limit: 0, Reason: reason}
		}
		if component.Limit < final.Limit {
			final = FinalLimit{Limit: component.Limit, Reason: reason}
		}
	}
	return final
}

func limitComponentReason(reason string) string {
	if reason == "" {
		return "none"
	}
	return reason
}
