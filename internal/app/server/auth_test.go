package server

import "testing"

func TestSignaturePath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: "/v1/signature/chatcmpl-1", want: true},
		{path: "/signature/chatcmpl-1", want: true},
		{path: "/v1/signature", want: false},
		{path: "/v1/signature/", want: false},
		{path: "/v1/chat/completions", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := signaturePath(tt.path); got != tt.want {
				t.Fatalf("signaturePath(%q)=%t want %t", tt.path, got, tt.want)
			}
		})
	}
}
