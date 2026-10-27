package request

import (
	"strings"
	"testing"
)

func chatBody(maxTokens, content string) []byte {
	return []byte(`{"model":"m","max_tokens":` + maxTokens + `,"messages":[{"role":"user","content":"` + content + `"}]}`)
}

func sharedBlocks(a, b PromptFingerprint) int {
	n := 0
	for n < len(a.Blocks) && n < len(b.Blocks) && a.Blocks[n] == b.Blocks[n] {
		n++
	}
	return n
}

func TestPromptFingerprintMatchesConversationPrefix(t *testing.T) {
	history := strings.Repeat("earlier turn ", 1000) // about 13 KiB
	first := fingerprintPrompt(chatBody("100", history))
	next := fingerprintPrompt(chatBody("4000", history+strings.Repeat("new turn ", 300)))
	if first.Bytes <= 0 || len(first.Blocks) == 0 {
		t.Fatalf("empty fingerprint: %+v", first)
	}
	// Every full block of the earlier Prompt is shared; sampling options
	// before "messages" do not matter.
	if got, want := sharedBlocks(first, next), len(first.Blocks)-1; got < want {
		t.Fatalf("shared blocks=%d want at least %d", got, want)
	}
}

func TestPromptFingerprintSeparatesDifferentPrompts(t *testing.T) {
	a := fingerprintPrompt(chatBody("1", strings.Repeat("a", 5000)))
	b := fingerprintPrompt(chatBody("1", strings.Repeat("b", 5000)))
	if sharedBlocks(a, b) != 0 {
		t.Fatal("different Prompts shared a block")
	}
}

func TestPromptFingerprintBlockCount(t *testing.T) {
	body := chatBody("1", strings.Repeat("x", 3*PromptBlockBytes))
	fp := fingerprintPrompt(body)
	if want := int((fp.Bytes + PromptBlockBytes - 1) / PromptBlockBytes); len(fp.Blocks) != want {
		t.Fatalf("blocks=%d want %d for %d bytes", len(fp.Blocks), want, fp.Bytes)
	}
}
