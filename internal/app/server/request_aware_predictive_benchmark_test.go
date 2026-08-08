package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

func BenchmarkRequestAwareHTTPPreForwardProtection(b *testing.B) {
	adapter, _ := newRequestAwareAdapterTestFixture(b, 8_990, 0)
	srv := newRequestAwareHTTPTestServer(b, "http://127.0.0.1", adapter, "enforce")
	b.Cleanup(func() {
		if err := srv.Close(); err != nil {
			b.Errorf("close benchmark server: %v", err)
		}
	})
	body := `{"model":"model-agnostic","messages":[{"role":"user","content":"` +
		strings.Repeat("a", 3_600) + `"}],"max_tokens":8}`

	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer secret")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		srv.ServeHTTP(response, request)
		if response.Code != http.StatusTooManyRequests {
			b.Fatalf("pre-forward protection status=%d body=%q, want 429", response.Code, response.Body.String())
		}
	}
}

func BenchmarkRequestAwareHTTPPreForwardProtectionByReservations(b *testing.B) {
	const reservationTokens = int64(16)
	for _, activeReservations := range []int{0, 48, 256} {
		b.Run(fmt.Sprintf("active-%d", activeReservations), func(b *testing.B) {
			usedTokens := int64(8_990) - int64(activeReservations)*reservationTokens
			adapter, manager := newRequestAwareAdapterTestFixture(b, usedTokens, 0)
			seeded := make([]predictiveShadowReservation, 0, activeReservations)
			for index := range activeReservations {
				decision := adapter.Decide(
					context.Background(),
					fmt.Sprintf("benchmark-seed-%d", index),
					requestAwareAdapterInput(1, 0),
				)
				if decision.Reservation == nil || !decision.Reservation.MarkForwarded() ||
					!decision.Reservation.MarkPrefillComplete() {
					b.Fatalf("seed reservation %d failed: %+v", index, decision)
				}
				seeded = append(seeded, decision.Reservation)
			}
			if snapshot := manager.Snapshot(); snapshot.Reservations != activeReservations ||
				snapshot.Virtual.Upper.ActiveKVUpper != 8_990 {
				b.Fatalf("seeded Manager state=%+v", snapshot)
			}

			srv := newRequestAwareHTTPTestServer(b, "http://127.0.0.1", adapter, "enforce")
			b.Cleanup(func() {
				if err := srv.Close(); err != nil {
					b.Errorf("close benchmark server: %v", err)
				}
				for _, reservation := range seeded {
					reservation.Terminate(runtimepredictive.TerminalExpired)
				}
			})
			body := `{"model":"model-agnostic","messages":[{"role":"user","content":"` +
				strings.Repeat("a", 3_600) + `"}],"max_tokens":8}`

			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			b.ResetTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
				request.Header.Set("Authorization", "Bearer secret")
				request.Header.Set("Content-Type", "application/json")
				response := httptest.NewRecorder()
				srv.ServeHTTP(response, request)
				if response.Code != http.StatusTooManyRequests {
					b.Fatalf("pre-forward protection status=%d body=%q, want 429", response.Code, response.Body.String())
				}
			}
		})
	}
}
