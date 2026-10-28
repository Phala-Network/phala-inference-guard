//go:build linux

package server

import (
	"fmt"
	"io"
	"os"
	"syscall"
)

func writeProcessFDMetrics(w io.Writer) {
	var limits syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limits); err != nil {
		fmt.Fprintln(w, "pig_process_fd_metrics_available 0")
		return
	}
	directory, err := os.Open("/proc/self/fd")
	if err != nil {
		fmt.Fprintln(w, "pig_process_fd_metrics_available 0")
		return
	}
	count := 0
	for {
		names, readErr := directory.Readdirnames(256)
		count += len(names)
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			err = readErr
			break
		}
	}
	_ = directory.Close()
	if err != nil {
		fmt.Fprintln(w, "pig_process_fd_metrics_available 0")
		return
	}
	// The enumeration sees its own temporary directory FD; exclude it. Other
	// live descriptors may change concurrently, so this is a point-in-time gauge.
	fmt.Fprintln(w, "pig_process_fd_metrics_available 1")
	fmt.Fprintf(w, "pig_process_open_fds %d\n", count-1)
	fmt.Fprintf(w, "pig_process_max_fds %d\n", limits.Cur)
	fmt.Fprintf(w, "pig_process_hard_max_fds %d\n", limits.Max)
}
