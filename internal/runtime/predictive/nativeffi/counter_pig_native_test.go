//go:build pig_native && cgo

package nativeffi

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

func TestNativeCounterReturnsOnlyExactTokenCountAndClosesIdempotently(t *testing.T) {
	counter, err := OpenCounter(validCounterConfig())
	if err != nil {
		t.Fatalf("open native counter: %v", err)
	}

	analysis, err := counter.Count(
		context.Background(),
		runtimepredictive.RequestClassCompletion,
		[]byte("hello world"),
		runtimepredictive.RequestFeatures{},
	)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if analysis != (runtimepredictive.TokenCountAnalysis{
		ManifestID:       "go-count-test-manifest",
		BackendEpoch:     "go-count-test-epoch",
		ExactInputTokens: 2,
	}) {
		t.Fatalf("count analysis = %+v", analysis)
	}
	if err := counter.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := counter.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if _, err := counter.Count(context.Background(), runtimepredictive.RequestClassCompletion, []byte("hello"), runtimepredictive.RequestFeatures{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("count after close error = %v, want unavailable", err)
	}
}

func TestNativeCounterRejectsInvalidConfigAndInputBeforeReturningAnalysis(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*CounterConfig)
		message string
	}{
		{name: "tokenizer path", mutate: func(config *CounterConfig) { config.TokenizerPath = "" }, message: "tokenizer path"},
		{name: "manifest", mutate: func(config *CounterConfig) { config.ManifestID = "" }, message: "manifest id"},
		{name: "backend epoch", mutate: func(config *CounterConfig) { config.BackendEpoch = "" }, message: "backend epoch"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validCounterConfig()
			test.mutate(&config)
			if _, err := OpenCounter(config); err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("OpenCounter error = %v, want %q", err, test.message)
			}
		})
	}
	config := validCounterConfig()
	config.TokenizerPath = filepath.Join(t.TempDir(), "missing-tokenizer.json")
	if _, err := OpenCounter(config); err == nil {
		t.Fatal("OpenCounter with missing tokenizer unexpectedly succeeded")
	}

	counter, err := OpenCounter(validCounterConfig())
	if err != nil {
		t.Fatalf("open native counter: %v", err)
	}
	defer counter.Close()

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := counter.Count(cancelled, runtimepredictive.RequestClassCompletion, []byte("hello"), runtimepredictive.RequestFeatures{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled count error = %v, want context canceled", err)
	}
	if _, err := counter.Count(context.Background(), runtimepredictive.RequestClass("unsupported"), []byte("hello"), runtimepredictive.RequestFeatures{}); !errors.Is(err, runtimepredictive.ErrUnsupportedRequestClass) {
		t.Fatalf("unsupported class error = %v", err)
	}
	if _, err := counter.Count(context.Background(), runtimepredictive.RequestClassCompletion, []byte{0xff}, runtimepredictive.RequestFeatures{}); err == nil || !strings.Contains(err.Error(), "valid UTF-8") {
		t.Fatalf("invalid UTF-8 error = %v", err)
	}
	var nilCounter *Counter
	if _, err := nilCounter.Count(context.Background(), runtimepredictive.RequestClassCompletion, []byte("hello"), runtimepredictive.RequestFeatures{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("nil counter error = %v, want unavailable", err)
	}
}

func TestNativeCounterCloseRacesDoNotLeakOrUseDestroyedHandle(t *testing.T) {
	counter, err := OpenCounter(validCounterConfig())
	if err != nil {
		t.Fatalf("open native counter: %v", err)
	}

	start := make(chan struct{})
	errorsByWorker := make(chan error, 16)
	var workers sync.WaitGroup
	for range 16 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			for range 32 {
				_, countErr := counter.Count(context.Background(), runtimepredictive.RequestClassCompletion, []byte("hello world"), runtimepredictive.RequestFeatures{})
				if countErr != nil && !errors.Is(countErr, ErrUnavailable) {
					errorsByWorker <- countErr
					return
				}
			}
		}()
	}
	close(start)
	if err := counter.Close(); err != nil {
		t.Fatalf("close counter: %v", err)
	}
	workers.Wait()
	close(errorsByWorker)
	for err := range errorsByWorker {
		t.Fatalf("concurrent count error = %v", err)
	}
	if _, err := counter.Count(context.Background(), runtimepredictive.RequestClassCompletion, []byte("hello"), runtimepredictive.RequestFeatures{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("post-close count error = %v, want unavailable", err)
	}
}

func validCounterConfig() CounterConfig {
	return CounterConfig{
		TokenizerPath: filepath.Join("..", "..", "..", "..", "native", "tokenizer", "fixtures", "ffi-wordlevel-tokenizer.json"),
		ManifestID:    "go-count-test-manifest",
		BackendEpoch:  "go-count-test-epoch",
	}
}
