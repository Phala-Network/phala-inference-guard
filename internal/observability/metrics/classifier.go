package metrics

import (
	"fmt"
	"io"

	"github.com/Phala-Network/phala-inference-guard/internal/support/num"
)

type ClassifierInput struct {
	Enabled       bool
	BodyBytes     int64
	Limit         int
	Inflight      int64
	RejectedTotal uint64
	Paths         []string
}

func WriteClassifier(w io.Writer, input ClassifierInput) {
	fmt.Fprintf(w, "pig_json_classify_enabled %d\n", num.BoolAsInt(input.Enabled))
	fmt.Fprintf(w, "pig_json_classify_body_bytes %d\n", input.BodyBytes)
	fmt.Fprintf(w, "pig_json_classifier_limit %d\n", input.Limit)
	fmt.Fprintf(w, "pig_json_classifier_inflight %d\n", input.Inflight)
	fmt.Fprintf(w, "pig_json_classifier_rejected_total %d\n", input.RejectedTotal)
	for _, path := range input.Paths {
		fmt.Fprintf(w, "pig_path_info{path=%q} 1\n", path)
	}
}
