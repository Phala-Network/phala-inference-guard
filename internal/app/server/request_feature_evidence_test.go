package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestFeatureMetricsCountClassifiedRequestsWithBoundedLabels(t *testing.T) {
	var seenBodies []string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBodies = append(seenBodies, string(body))
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	srv, err := newTestProxyServer(testProxyConfig(backend.URL))
	if err != nil {
		t.Fatalf("newProxyServer: %v", err)
	}
	var empty strings.Builder
	srv.writeLocalMetrics(&empty)

	requests := []struct {
		path string
		body string
	}{
		{"/v1/chat/completions", `{"model":"secret-model","messages":[],"logprobs":true,"top_logprobs":3,` +
			`"tools":[{"type":"function","function":{"name":"secret_fn"}}],` +
			`"tool_choice":{"type":"function","function":{"name":"secret_fn"}},"response_format":{"type":"json_object"}}`},
		{"/v1/chat/completions", `{"messages":[],"tool_choice":"secret_choice"}`},
		{"/v1/completions", `{"prompt":"p","logprobs":0,"regex":"[a-z]+"}`},
		{"/v1/responses", `{"input":"x","text":{"format":{"type":"json_schema","name":"secret_schema","schema":{}}}}`},
		{"/v1/chat/completions", `{"messages":[`},
	}
	for _, item := range requests {
		request := httptest.NewRequest(http.MethodPost, item.path, strings.NewReader(item.body))
		request.Header.Set("Authorization", "Bearer secret")
		request.Header.Set("Content-Type", "application/json")
		srv.ServeHTTP(httptest.NewRecorder(), request)
	}
	if len(seenBodies) != 4 {
		t.Fatalf("backend saw %d requests, want 4 valid requests forwarded", len(seenBodies))
	}
	for index, body := range seenBodies {
		if body != requests[index].body {
			t.Fatalf("forwarded body=%s want %s", body, requests[index].body)
		}
	}

	var rendered strings.Builder
	srv.writeLocalMetrics(&rendered)
	metricsBody := rendered.String()
	if strings.Count(empty.String(), "\n") != strings.Count(metricsBody, "\n") {
		t.Fatalf("request features changed metric cardinality: empty=%d recorded=%d",
			strings.Count(empty.String(), "\n"), strings.Count(metricsBody, "\n"))
	}
	for _, secret := range []string{"secret-model", "secret_fn", "secret_choice", "secret_schema"} {
		if strings.Contains(metricsBody, secret) {
			t.Fatalf("request content %q escaped into metrics", secret)
		}
	}
	for _, want := range []string{
		`pig_request_feature_logprobs_total{endpoint="chat_completions",top_logprobs="1-5"} 1`,
		`pig_request_feature_logprobs_total{endpoint="chat_completions",top_logprobs="none"} 1`,
		`pig_request_feature_tool_choice_total{endpoint="chat_completions",tool_choice="named"} 1`,
		`pig_request_feature_tool_choice_total{endpoint="chat_completions",tool_choice="other"} 1`,
		`pig_request_feature_tools_total{endpoint="chat_completions",tools="present"} 1`,
		`pig_request_feature_tools_total{endpoint="chat_completions",tools="absent"} 1`,
		`pig_request_feature_response_format_total{endpoint="chat_completions",format="json_object"} 1`,
		`pig_request_feature_response_format_total{endpoint="chat_completions",format="unspecified"} 1`,
		`pig_request_feature_logprobs_total{endpoint="completions",top_logprobs="0"} 1`,
		`pig_request_feature_response_format_total{endpoint="completions",format="regex"} 1`,
		`pig_request_feature_tool_choice_total{endpoint="completions",tool_choice="unspecified"} 1`,
		`pig_request_feature_response_format_total{endpoint="responses",format="json_schema"} 1`,
		`pig_request_feature_logprobs_total{endpoint="responses",top_logprobs="none"} 1`,
	} {
		if !strings.Contains(metricsBody, want+"\n") {
			t.Fatalf("request feature metrics missing %q\n%s", want, metricsBody)
		}
	}
}
