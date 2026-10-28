//go:build linux

package server

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func fdPath(fd uintptr) string { return "/proc/self/fd/" + strconv.FormatUint(uint64(fd), 10) }

func TestProcessFDClassifiesRealDescriptors(t *testing.T) {
	regular, err := os.CreateTemp(t.TempDir(), "fd-private-path-")
	if err != nil {
		t.Fatal(err)
	}
	defer regular.Close()
	device, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer device.Close()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()
	event, err := syscall.EpollCreate1(syscall.EPOLL_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(event)
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	for _, tc := range []struct {
		fd   uintptr
		kind int
	}{
		{regular.Fd(), processFDRegular}, {device.Fd(), processFDDevice},
		{read.Fd(), processFDPipe}, {write.Fd(), processFDPipe},
		{uintptr(event), processFDAnonInode}, {directory.Fd(), processFDOther},
	} {
		kind, inode := classifyProcessFD(fdPath(tc.fd))
		if kind != tc.kind || inode != "" {
			t.Fatalf("kind=%d expected=%d", kind, tc.kind)
		}
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	raw, err := listener.(*net.TCPListener).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Control(func(fd uintptr) {
		kind, inode := classifyProcessFD(fdPath(fd))
		if kind != processFDSocket || inode == "" {
			t.Error("real socket was not classified")
		}
		counts, available := collectProcessSocketStates("/proc/self/net", map[string]int{inode: 1})
		if !available || counts[processSocketListen] != 1 {
			t.Errorf("listener states=%v available=%t", counts, available)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if kind, _ := classifyProcessFD("/proc/self/fd/not-a-descriptor"); kind != processFDOther {
		t.Fatal("missing descriptor did not remain other")
	}
}

func socketTableRow(inode, state string) string {
	// Addresses and unrelated inodes are deliberately present but must never be output.
	return "0: 0100007F:ABCD 0100007F:1234 " + state + " 0:0 00:0 0 1000 0 " + inode + " 1\n"
}

func TestProcessFDSocketStatesOwnInodesAndUnavailable(t *testing.T) {
	root := t.TempDir()
	header := "sl local_address rem_address st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode\n"
	if err := os.WriteFile(filepath.Join(root, "tcp"), []byte(header+socketTableRow("11", "01")+socketTableRow("12", "08")+socketTableRow("999999", "01")), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tcp6"), []byte(header+socketTableRow("13", "0A")+socketTableRow("14", "05")), 0600); err != nil {
		t.Fatal(err)
	}
	owned := map[string]int{"11": 2, "12": 1, "13": 1, "14": 1, "15": 1}
	counts, available := collectProcessSocketStates(root, owned)
	if !available || counts != [processSocketStateCount]int{2, 1, 1, 1, 1} {
		t.Fatalf("states=%v available=%t", counts, available)
	}
	if len(owned) != 5 {
		t.Fatal("input inode counts mutated")
	}
	if err := os.Remove(filepath.Join(root, "tcp6")); err != nil {
		t.Fatal(err)
	}
	counts, available = collectProcessSocketStates(root, owned)
	if available || counts != [processSocketStateCount]int{2, 1, 0, 0, 3} {
		t.Fatalf("missing table states=%v available=%t", counts, available)
	}
	if err := os.WriteFile(filepath.Join(root, "tcp"), []byte("not a table"), 0600); err != nil {
		t.Fatal(err)
	}
	counts, available = collectProcessSocketStates(root, owned)
	if available || counts[processSocketUnresolved] != 6 {
		t.Fatalf("invalid table states=%v available=%t", counts, available)
	}
}

func TestProcessFDSocketTableReadBound(t *testing.T) {
	root := t.TempDir()
	header := "sl local_address rem_address st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode\n"
	row := socketTableRow("999", "01")
	body := header + strings.Repeat(row, maximumSocketTableBytes/len(row)+1)
	if err := os.WriteFile(filepath.Join(root, "tcp"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tcp6"), []byte(header), 0600); err != nil {
		t.Fatal(err)
	}
	counts, available := collectProcessSocketStates(root, map[string]int{"123": 1})
	if available || counts[processSocketUnresolved] != 1 {
		t.Fatalf("bounded scan states=%v available=%t", counts, available)
	}
}

func TestProcessFDSocketStatesNoSocketsAndBadRows(t *testing.T) {
	root := t.TempDir()
	counts, available := collectProcessSocketStates(root, nil)
	if !available || counts != [processSocketStateCount]int{} {
		t.Fatal("empty socket set should not need network tables")
	}
	header := "sl local_address rem_address st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode\n"
	for _, row := range []string{"bad row\n", socketTableRow("11", "not-a-state")} {
		if err := os.WriteFile(filepath.Join(root, "tcp"), []byte(header+row), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "tcp6"), []byte(header), 0600); err != nil {
			t.Fatal(err)
		}
		counts, available = collectProcessSocketStates(root, map[string]int{"11": 1})
		if available || counts[processSocketUnresolved] != 1 {
			t.Fatalf("bad row states=%v available=%t", counts, available)
		}
	}
}

func TestProcessFDSnapshotReadersCloseAndExcludeOwnDirectory(t *testing.T) {
	before, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 20; index++ {
		snapshot := collectProcessFDSnapshot()
		if !snapshot.available {
			t.Fatal("FD snapshot unavailable")
		}
		totalSockets := 0
		for _, count := range snapshot.socketStates {
			totalSockets += count
		}
		if totalSockets != snapshot.types[processFDSocket] {
			t.Fatal("socket state total differs from same-snapshot socket FDs")
		}
		if snapshot.types[processFDOther] != 0 {
			t.Fatal("collector directory was retained in FD classification")
		}
	}
	after, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("reader FDs retained: before=%d after=%d", len(before), len(after))
	}
}

func TestProcessFDCacheConcurrentExpiryAndUnavailable(t *testing.T) {
	var calls atomic.Int32
	cache := processFDCache{collect: func() processFDSnapshot {
		call := calls.Add(1)
		return processFDSnapshot{available: call != 2, types: [processFDTypeCount]int{int(call)}}
	}}
	now := time.Unix(100, 0)
	var group sync.WaitGroup
	for index := 0; index < 64; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if cache.snapshot(now).count() != 1 {
				t.Error("cache returned inconsistent snapshot")
			}
		}()
	}
	group.Wait()
	if calls.Load() != 1 {
		t.Fatalf("concurrent scans=%d", calls.Load())
	}
	if !cache.snapshot(now.Add(4*time.Second)).available || calls.Load() != 1 {
		t.Fatal("fresh snapshot rescanned")
	}
	if !cache.snapshot(now.Add(-time.Nanosecond)).available || calls.Load() != 1 {
		t.Fatal("out-of-order caller rescanned")
	}
	if cache.snapshot(now.Add(5*time.Second)).available || calls.Load() != 2 {
		t.Fatal("expired failure was hidden")
	}
	if cache.snapshot(now.Add(6*time.Second)).available || calls.Load() != 2 {
		t.Fatal("unavailable snapshot stampede")
	}
	if !cache.snapshot(now.Add(10*time.Second)).available || calls.Load() != 3 {
		t.Fatal("collector did not retry after expiry")
	}
}

func TestProcessFDMetricsFixedLabelsSumsPrivacyAndFailure(t *testing.T) {
	now := time.Unix(100, 0)
	snapshot := processFDSnapshot{capturedAt: now, available: true, softLimit: 1024, hardLimit: 2048,
		types: [processFDTypeCount]int{5, 1, 2, 2, 3, 1}, socketStates: [processSocketStateCount]int{2, 1, 1, 0, 1}}
	var output bytes.Buffer
	writeProcessFDSnapshot(&output, snapshot, now.Add(2*time.Second))
	text := output.String()
	for _, expected := range []string{"pig_process_open_fds 14\n", "pig_process_fd_snapshot_age_seconds 2.000000\n", "pig_process_socket_state_metrics_available 0\n"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("missing fixed metric %q", expected)
		}
	}
	for kind, label := range processFDTypeLabels {
		if !strings.Contains(text, fmt.Sprintf("pig_process_open_fds_by_type{type=%q} %d\n", label, snapshot.types[kind])) {
			t.Fatal("fixed FD label missing")
		}
	}
	if strings.Contains(text, "path=") || strings.Contains(text, "inode=") || strings.Contains(text, "address=") || strings.Contains(text, "port=") {
		t.Fatal("sensitive dimension exported")
	}
	output.Reset()
	writeProcessFDSnapshot(&output, processFDSnapshot{}, now)
	if output.String() != "pig_process_fd_metrics_available 0\n" {
		t.Fatal("unavailable snapshot emitted zero counts")
	}
}
