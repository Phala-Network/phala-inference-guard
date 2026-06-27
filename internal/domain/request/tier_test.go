package request

import (
	"net/http"
	"testing"
)

func TestFromHeaderRequiresSingleTrustedTierValue(t *testing.T) {
	request := &http.Request{Header: make(http.Header)}
	request.Header.Add(Header, "premium")
	request.Header.Add(Header, "basic")

	if got := FromHeader(request); got != Basic {
		t.Fatalf("FromHeader with duplicate tier headers = %s, want basic", got)
	}
}

func TestFromHeaderRejectsCommaJoinedTierValues(t *testing.T) {
	request := &http.Request{Header: make(http.Header)}
	request.Header.Set(Header, "premium, basic")

	if got := FromHeader(request); got != Basic {
		t.Fatalf("FromHeader with comma-joined tier header = %s, want basic", got)
	}
}

func TestFromHeaderAcceptsSinglePremiumValue(t *testing.T) {
	request := &http.Request{Header: make(http.Header)}
	request.Header.Set(Header, " premium ")

	if got := FromHeader(request); got != Premium {
		t.Fatalf("FromHeader = %s, want premium", got)
	}
}
