package server

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func newAdmissionTestServer(t *testing.T) *proxyServer {
	t.Helper()
	backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(backend.Close)
	cfg := testProxyConfig(backend.URL)
	cfg.AdminToken = "admin-secret"
	cfg.GlobalLimit = 100
	cfg.DynamicGlobalGreen = 100
	cfg.DynamicGlobalYellow = 80
	cfg.DynamicGlobalRed = 50
	cfg.DynamicUserTPSCapacityRatio = .42
	cfg.DynamicUserTPSCapacityRatioMax = .85
	cfg.DynamicUserTPSCapacitySmoothing = .85
	cfg.DynamicUserTPSCapacityStepUp = .02
	cfg.DynamicUserTPSCapacityHealthyN = 10
	cfg.DynamicUserTPSCapacityHealthyMul = 1.5
	cfg.DynamicPressureLearnRatio = .8
	srv, err := newProxyServer(cfg)
	if err != nil {
		t.Fatalf("newProxyServer: %v", err)
	}
	return srv
}

func admissionRequest(t *testing.T, srv *proxyServer, method, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, admissionConfigPath, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	return w
}

func getAdmission(t *testing.T, srv *proxyServer) admissionConfigDocument {
	t.Helper()
	w := admissionRequest(t, srv, http.MethodGet, "admin-secret", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", w.Code, w.Body.String())
	}
	var d admissionConfigDocument
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.GlobalLimit == 0 {
		t.Fatal("GET omitted global_limit")
	}
	return d
}

func TestAdmissionConfigAuthAndAdminTokenPreference(t *testing.T) {
	srv := newAdmissionTestServer(t)
	for _, token := range []string{"", "secret", "wrong"} {
		if w := admissionRequest(t, srv, http.MethodGet, token, ""); w.Code != http.StatusUnauthorized {
			t.Fatalf("token %q status=%d want 401", token, w.Code)
		}
	}
	if w := admissionRequest(t, srv, http.MethodGet, "admin-secret", ""); w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	srv.cfg.AdminToken = ""
	if w := admissionRequest(t, srv, http.MethodGet, "secret", ""); w.Code != http.StatusOK {
		t.Fatalf("TOKEN fallback status=%d", w.Code)
	}
	srv.cfg.Token = ""
	if w := admissionRequest(t, srv, http.MethodGet, "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("empty auth status=%d", w.Code)
	}
}

func TestAdmissionConfigPutAndRevisionConflict(t *testing.T) {
	srv := newAdmissionTestServer(t)
	d := getAdmission(t, srv)
	d.GlobalLimit = 120
	d.GlobalGreen, d.GlobalYellow, d.GlobalRed = 90, 70, 40
	d.UserTPSCapacityRatio, d.UserTPSCapacityRatioMax = .5, .9
	d.KVYellow, d.KVRed = .6, .9
	d.RunningYellow, d.RunningRed = 12, 24
	d.WaitingYellow, d.WaitingRed = 2, 7
	d.PreemptRed = 3
	d.PressureHeadroom, d.PressureMinLimit = 4, 2
	d.UserTPSYellow, d.UserTPSRed = 25, 10
	d.UserTPSGraceMinSeconds, d.UserTPSGraceMaxSeconds = 1.25, 8.5
	d.TTFTTargetSeconds, d.TTFTRedSeconds = 1.2, 4
	body, _ := json.Marshal(d)
	w := admissionRequest(t, srv, http.MethodPut, "admin-secret", string(body))
	if w.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", w.Code, w.Body.String())
	}
	got := getAdmission(t, srv)
	if got.Revision != d.Revision+1 || got.GlobalLimit != 120 || got.GlobalGreen != 90 || got.UserTPSCapacityRatio != .5 || got.KVRed != .9 || got.RunningRed != 24 || got.UserTPSGraceMaxSeconds != 8.5 || got.TTFTRedSeconds != 4 {
		t.Fatalf("updated=%+v", got)
	}
	w = admissionRequest(t, srv, http.MethodPut, "admin-secret", string(body))
	if w.Code != http.StatusConflict {
		t.Fatalf("stale PUT status=%d body=%s", w.Code, w.Body.String())
	}
	if getAdmission(t, srv) != got {
		t.Fatal("conflicting update mutated config")
	}
}

func TestAdmissionConfigRejectsUnsafeJSONAndInvalidValues(t *testing.T) {
	srv := newAdmissionTestServer(t)
	d := getAdmission(t, srv)
	valid, _ := json.Marshal(d)
	cases := []struct {
		name, body string
		status     int
	}{
		{"unknown", strings.TrimSuffix(string(valid), "}") + `,"token":"leak"}`, http.StatusBadRequest},
		{"trailing", string(valid) + ` {}`, http.StatusBadRequest},
		{"duplicate-object", string(valid) + string(valid), http.StatusBadRequest},
		{"too-large", strings.Repeat(" ", maxAdmissionConfigBody+1), http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := admissionRequest(t, srv, http.MethodPut, "admin-secret", tc.body)
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}

	invalid := []func(*admissionConfigDocument){
		func(x *admissionConfigDocument) { x.GlobalLimit = -1 },
		func(x *admissionConfigDocument) { x.GlobalGreen = x.GlobalLimit + 1 },
		func(x *admissionConfigDocument) { x.GlobalYellow = x.GlobalGreen + 1 },
		func(x *admissionConfigDocument) { x.UserTPSCapacityRatio = 0 },
		func(x *admissionConfigDocument) { x.UserTPSCapacityRatioMax = x.UserTPSCapacityRatio - .1 },
		func(x *admissionConfigDocument) { x.UserTPSCapacitySmoothing = 1 },
		func(x *admissionConfigDocument) { x.UserTPSCapacityStepUp = 0 },
		func(x *admissionConfigDocument) { x.UserTPSCapacityHealthyN = 0 },
		func(x *admissionConfigDocument) { x.UserTPSCapacityHealthyMul = .9 },
		func(x *admissionConfigDocument) { x.KVRed = x.KVYellow - .1 },
		func(x *admissionConfigDocument) { x.RunningRed = x.RunningYellow - 1 },
		func(x *admissionConfigDocument) { x.WaitingRed = x.WaitingYellow - 1 },
		func(x *admissionConfigDocument) { x.PressureEnabled = true; x.PressureLearnRatio = 0 },
		func(x *admissionConfigDocument) { x.UserTPSEnabled = true; x.UserTPSRed = x.UserTPSYellow + 1 },
		func(x *admissionConfigDocument) { x.UserTPSGraceMaxSeconds = x.UserTPSGraceMinSeconds - 1 },
		func(x *admissionConfigDocument) {
			x.UserTPSGraceMaxSeconds = float64(^uint64(0)>>1)/float64(time.Second) + 1
		},
		func(x *admissionConfigDocument) { x.TTFTEnabled = true; x.TTFTRedSeconds = x.TTFTTargetSeconds - .1 },
		func(x *admissionConfigDocument) { x.TTFTEnabled = true; x.TTFTP99SignalWeight = 2 },
		func(x *admissionConfigDocument) { x.FailsafeState = "purple" },
	}
	for i, mutate := range invalid {
		x := d
		mutate(&x)
		body, _ := json.Marshal(x)
		if w := admissionRequest(t, srv, http.MethodPut, "admin-secret", string(body)); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid %d status=%d", i, w.Code)
		}
	}
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		x := d
		x.UserTPSCapacityRatio = value
		if err := validateAdmissionDocument(x); err == nil {
			t.Fatalf("non-finite %v accepted", value)
		}
	}
}

func TestAdmissionConfigConcurrentReadsAndWrites(t *testing.T) {
	srv := newAdmissionTestServer(t)
	const updates = 25
	var wg sync.WaitGroup
	errs := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < updates; j++ {
				w := admissionRequest(t, srv, http.MethodGet, "admin-secret", "")
				if w.Code != http.StatusOK {
					errs <- w.Body.String()
					return
				}
			}
		}()
	}
	for i := 0; i < updates; i++ {
		d := getAdmission(t, srv)
		d.GlobalRed = 40 + i%10
		body, _ := json.Marshal(d)
		if w := admissionRequest(t, srv, http.MethodPut, "admin-secret", string(body)); w.Code != http.StatusOK {
			t.Fatalf("PUT %d status=%d", i, w.Code)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent GET failed: %s", err)
	}
	if got := getAdmission(t, srv); got.Revision != 1+updates {
		t.Fatalf("revision=%d", got.Revision)
	}
}

func TestAdmissionConfigGlobalLimitZeroStopsNewAdmissionsImmediately(t *testing.T) {
	srv := newAdmissionTestServer(t)
	d := getAdmission(t, srv)
	d.GlobalLimit, d.GlobalGreen, d.GlobalYellow, d.GlobalRed = 0, 0, 0, 0
	body, _ := json.Marshal(d)
	if w := admissionRequest(t, srv, http.MethodPut, "admin-secret", string(body)); w.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", w.Code, w.Body.String())
	}
	if srv.globalLn.Limit() != 0 || srv.globalLn.TryAcquire() {
		t.Fatal("zero hard limit did not stop admission")
	}
	if got := srv.dynamicController.Snapshot(); got.Source != "runtime_config" || got.GlobalLimit != 0 {
		t.Fatalf("unsafe immediate snapshot: %+v", got)
	}
}

func TestAdmissionConfigIsProcessLocal(t *testing.T) {
	first := newAdmissionTestServer(t)
	d := getAdmission(t, first)
	d.GlobalGreen = 77
	d.GlobalYellow = 70
	d.GlobalRed = 40
	body, _ := json.Marshal(d)
	if w := admissionRequest(t, first, http.MethodPut, "admin-secret", string(body)); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	second := newAdmissionTestServer(t)
	if got := getAdmission(t, second); got.Revision != 1 || got.GlobalGreen != 100 {
		t.Fatalf("new process config=%+v", got)
	}
}
