package attestation

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestGenerateSerializesConcurrentGuestAgentCalls(t *testing.T) {
	// The fake agent serves one request at a time, 200 ms per quote, on a Unix
	// socket like the real one. Its total of 8 x 200 ms exceeds the 1 s
	// per-call deadline, so requests succeed only if PIG serializes them.
	var mu sync.Mutex
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		body := map[string]any{"app_id": "app"}
		if r.URL.Path == "/GetQuote" {
			time.Sleep(200 * time.Millisecond)
			body = map[string]any{"quote": "q", "event_log": "[]"}
		}
		if err := json.NewEncoder(w).Encode(body); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
	// Unix socket paths are length-limited, so avoid the long t.TempDir path.
	dir, err := os.MkdirTemp("", "pig-dstack")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "dstack.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	service, err := NewService(Config{GPUArch: "HOPPER"}, NewDstackClient(socket, time.Second))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	service.nvidia = nil

	const requests = 8
	errs := make(chan error, requests)
	for range requests {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, err := service.Generate(ctx, ReportRequest{SigningAlgo: AlgoECDSA, Version: 1})
			errs <- err
		}()
	}
	for range requests {
		if err := <-errs; err != nil {
			t.Errorf("Generate: %v", err)
		}
	}
}

type blockingDstack struct {
	mockDstack
	entered chan struct{}
	release chan struct{}
}

func (b *blockingDstack) GetQuote(ctx context.Context, reportData []byte) (QuoteResponse, error) {
	b.entered <- struct{}{}
	<-b.release
	return b.mockDstack.GetQuote(ctx, reportData)
}

func TestGenerateWaitingRequestFailsWhenContextEnds(t *testing.T) {
	dstack := &blockingDstack{entered: make(chan struct{}, 1), release: make(chan struct{})}
	service, err := NewService(Config{GPUArch: "HOPPER"}, dstack)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	service.nvidia = nil

	first := make(chan error, 1)
	go func() {
		_, err := service.Generate(context.Background(), ReportRequest{SigningAlgo: AlgoECDSA, Version: 1})
		first <- err
	}()
	<-dstack.entered

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := service.Generate(ctx, ReportRequest{SigningAlgo: AlgoECDSA, Version: 1}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting Generate error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("waiting Generate took %s, want fail fast", elapsed)
	}

	close(dstack.release)
	if err := <-first; err != nil {
		t.Fatalf("first Generate: %v", err)
	}
}
