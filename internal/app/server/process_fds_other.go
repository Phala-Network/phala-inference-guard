//go:build !linux

package server

import (
	"fmt"
	"io"
)

func writeProcessFDMetrics(w io.Writer) {
	fmt.Fprintln(w, "pig_process_fd_metrics_available 0")
}
