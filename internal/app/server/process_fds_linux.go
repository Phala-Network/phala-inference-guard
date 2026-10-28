//go:build linux

package server

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const processFDCacheDuration = 5 * time.Second
const maximumSocketTableBytes = 4 * 1024 * 1024

const (
	processFDSocket = iota
	processFDDevice
	processFDPipe
	processFDAnonInode
	processFDRegular
	processFDOther
	processFDTypeCount
)

const (
	processSocketEstablished = iota
	processSocketCloseWait
	processSocketListen
	processSocketOther
	processSocketUnresolved
	processSocketStateCount
)

var processFDTypeLabels = [...]string{"socket", "device", "pipe", "anon_inode", "regular", "other"}
var processSocketStateLabels = [...]string{"ESTABLISHED", "CLOSE_WAIT", "LISTEN", "other", "unresolved"}

type processFDSnapshot struct {
	capturedAt            time.Time
	available             bool
	softLimit             uint64
	hardLimit             uint64
	types                 [processFDTypeCount]int
	socketStates          [processSocketStateCount]int
	socketStatesAvailable bool
}

func (s processFDSnapshot) count() int {
	total := 0
	for _, value := range s.types {
		total += value
	}
	return total
}

type processFDCache struct {
	mu      sync.Mutex
	current processFDSnapshot
	collect func() processFDSnapshot
}

func (c *processFDCache) snapshot(now time.Time) processFDSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.current.capturedAt.IsZero() && now.Sub(c.current.capturedAt) < processFDCacheDuration {
		return c.current
	}
	c.current = c.collect()
	c.current.capturedAt = now
	return c.current
}

var processFDMetricsCache = processFDCache{collect: collectProcessFDSnapshot}

func collectProcessFDSnapshot() (snapshot processFDSnapshot) {
	var limits syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limits); err != nil {
		return
	}
	directory, err := os.Open("/proc/self/fd")
	if err != nil {
		return
	}
	defer directory.Close()
	ownFD := strconv.FormatUint(uint64(directory.Fd()), 10)
	sockets := make(map[string]int)
	for {
		names, readErr := directory.Readdirnames(256)
		for _, name := range names {
			if name == ownFD {
				continue
			}
			kind, inode := classifyProcessFD(filepath.Join("/proc/self/fd", name))
			snapshot.types[kind]++
			if kind == processFDSocket {
				sockets[inode]++
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return processFDSnapshot{}
		}
	}
	snapshot.available = true
	snapshot.softLimit, snapshot.hardLimit = limits.Cur, limits.Max
	snapshot.socketStates, snapshot.socketStatesAvailable = collectProcessSocketStates("/proc/self/net", sockets)
	return snapshot
}

// Keep targets local to classification. Never export paths, FD numbers, socket
// inodes, addresses or ports. A disappearing FD stays in this enumeration's
// total, classified as other.
func classifyProcessFD(path string) (kind int, inode string) {
	target, err := os.Readlink(path)
	if err != nil {
		return processFDOther, ""
	}
	if strings.HasPrefix(target, "socket:[") && strings.HasSuffix(target, "]") {
		return processFDSocket, strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")
	}
	if strings.HasPrefix(target, "pipe:[") {
		return processFDPipe, ""
	}
	if strings.HasPrefix(target, "anon_inode:") {
		return processFDAnonInode, ""
	}
	info, err := os.Stat(path)
	if err != nil {
		return processFDOther, ""
	}
	if info.Mode()&os.ModeDevice != 0 {
		return processFDDevice, ""
	}
	if info.Mode().IsRegular() {
		return processFDRegular, ""
	}
	return processFDOther, ""
}

// Match only socket inodes from this process's FD snapshot. TCP tables have a
// byte bound and are never retained/exported. Unix/UDP sockets and races remain
// unresolved; they must not be inferred to have a TCP state.
func collectProcessSocketStates(root string, sockets map[string]int) ([processSocketStateCount]int, bool) {
	var counts [processSocketStateCount]int
	remaining := make(map[string]int, len(sockets))
	for inode, count := range sockets {
		remaining[inode] = count
		counts[processSocketUnresolved] += count
	}
	available := true
	for _, table := range []string{"tcp", "tcp6"} {
		if len(remaining) == 0 {
			break
		}
		if !readProcessSocketTable(filepath.Join(root, table), remaining, &counts) {
			available = false
		}
	}
	return counts, available
}

func readProcessSocketTable(path string, remaining map[string]int, counts *[processSocketStateCount]int) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	limited := &io.LimitedReader{R: file, N: maximumSocketTableBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), 4096)
	if !scanner.Scan() || !strings.Contains(scanner.Text(), "inode") {
		return false
	}
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 10 {
			return false
		}
		count, ok := remaining[fields[9]]
		if !ok {
			continue
		}
		if _, err := strconv.ParseUint(fields[3], 16, 8); err != nil {
			return false
		}
		state := processSocketOther
		switch fields[3] {
		case "01":
			state = processSocketEstablished
		case "08":
			state = processSocketCloseWait
		case "0A":
			state = processSocketListen
		}
		counts[state] += count
		counts[processSocketUnresolved] -= count
		delete(remaining, fields[9])
		if len(remaining) == 0 {
			return true
		}
	}
	return scanner.Err() == nil && limited.N > 0
}

func writeProcessFDMetrics(w io.Writer) {
	snapshot := processFDMetricsCache.snapshot(time.Now())
	writeProcessFDSnapshot(w, snapshot, time.Now())
}

func writeProcessFDSnapshot(w io.Writer, snapshot processFDSnapshot, now time.Time) {
	if !snapshot.available {
		fmt.Fprintln(w, "pig_process_fd_metrics_available 0")
		return
	}
	fmt.Fprintln(w, "pig_process_fd_metrics_available 1")
	fmt.Fprintf(w, "pig_process_open_fds %d\n", snapshot.count())
	fmt.Fprintf(w, "pig_process_max_fds %d\n", snapshot.softLimit)
	fmt.Fprintf(w, "pig_process_hard_max_fds %d\n", snapshot.hardLimit)
	age := now.Sub(snapshot.capturedAt)
	if age < 0 {
		age = 0
	}
	fmt.Fprintf(w, "pig_process_fd_snapshot_age_seconds %.6f\n", age.Seconds())
	for kind, label := range processFDTypeLabels {
		fmt.Fprintf(w, "pig_process_open_fds_by_type{type=%q} %d\n", label, snapshot.types[kind])
	}
	available := 0
	if snapshot.socketStatesAvailable {
		available = 1
	}
	fmt.Fprintf(w, "pig_process_socket_state_metrics_available %d\n", available)
	for state, label := range processSocketStateLabels {
		fmt.Fprintf(w, "pig_process_socket_fds_by_tcp_state{state=%q} %d\n", label, snapshot.socketStates[state])
	}
}
