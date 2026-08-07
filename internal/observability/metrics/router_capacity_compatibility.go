package metrics

import (
	"fmt"
	"io"
)

// RouterCapacityCompatibility is the narrow wire contract required by the
// current Router parser. These values are projections of one predictive
// observer snapshot; they do not reintroduce a dynamic request-count policy.
type RouterCapacityCompatibility struct {
	ObservedRunningRaw int
	ObservedWaitingRaw int
	ObservedRunning    int
	ObservedWaiting    int
	GlobalLimitRaw     int
	GlobalLimit        int
}

func WriteRouterCapacityCompatibility(w io.Writer, value RouterCapacityCompatibility) {
	fmt.Fprintf(w, "pig_dynamic_observed_running_raw %d\n", value.ObservedRunningRaw)
	fmt.Fprintf(w, "pig_dynamic_observed_waiting_raw %d\n", value.ObservedWaitingRaw)
	fmt.Fprintf(w, "pig_dynamic_observed_running %d\n", value.ObservedRunning)
	fmt.Fprintf(w, "pig_dynamic_observed_waiting %d\n", value.ObservedWaiting)
	fmt.Fprintf(w, "pig_dynamic_global_limit_raw %d\n", value.GlobalLimitRaw)
	fmt.Fprintf(w, "pig_dynamic_global_limit %d\n", value.GlobalLimit)
}
