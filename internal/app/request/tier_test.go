package request

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTierValueRequiresOneHeader(t *testing.T) {
	tests := []struct {
		name   string
		values []string
		want   string
	}{
		{name: "single", values: []string{"priority"}, want: "priority"},
		{name: "trimmed", values: []string{"  flex  "}, want: "flex"},
		{name: "missing", want: ""},
		{name: "duplicate", values: []string{"priority", "default"}, want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			for _, value := range test.values {
				r.Header.Add(UserTierHeader, value)
			}
			if got := TierValue(r); got != test.want {
				t.Fatalf("TierValue=%q want %q", got, test.want)
			}
		})
	}
}

func TestTierValueNilRequestIsEmpty(t *testing.T) {
	if got := TierValue(nil); got != "" {
		t.Fatalf("nil request tier=%q", got)
	}
}
