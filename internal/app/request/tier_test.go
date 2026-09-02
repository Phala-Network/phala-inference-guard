package request

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsPremiumTierRequiresOneUnambiguousValue(t *testing.T) {
	tests := []struct {
		name   string
		values []string
		want   bool
	}{
		{name: "canonical", values: []string{"premium"}, want: true},
		{name: "case and whitespace", values: []string{"  PrEmIuM  "}, want: true},
		{name: "missing", want: false},
		{name: "basic", values: []string{"basic"}, want: false},
		{name: "unknown", values: []string{"premiumish"}, want: false},
		{name: "duplicate", values: []string{"premium", "basic"}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			for _, value := range test.values {
				r.Header.Add(UserTierHeader, value)
			}
			if got := IsPremiumTier(r); got != test.want {
				t.Fatalf("IsPremiumTier=%t want %t", got, test.want)
			}
		})
	}
}

func TestIsPremiumTierNilRequestIsFalse(t *testing.T) {
	if IsPremiumTier(nil) {
		t.Fatal("nil request was classified as premium")
	}
}
