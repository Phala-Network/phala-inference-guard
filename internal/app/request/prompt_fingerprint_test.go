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
	next := fingerprintPrompt(chatBody("100", history+strings.Repeat("new turn ", 300)))
	if first.Bytes <= 0 || len(first.Blocks) == 0 {
		t.Fatalf("empty fingerprint: %+v", first)
	}
	// Every full block of an unchanged request prefix is shared.
	if got, want := sharedBlocks(first, next), len(first.Blocks)-1; got < want {
		t.Fatalf("shared blocks=%d want at least %d", got, want)
	}
}

func TestPromptFingerprintIncludesLeadingRenderingFields(t *testing.T) {
	messages := `"messages":[{"role":"user","content":"` + strings.Repeat("same prompt ", 600) + `"}]}`
	for _, field := range []string{"tools", "chat_template_kwargs", "model"} {
		a := fingerprintPrompt([]byte(`{"` + field + `":"first",` + messages))
		b := fingerprintPrompt([]byte(`{"` + field + `":"other",` + messages))
		if sharedBlocks(a, b) != 0 {
			t.Fatalf("changed leading %s retained a warm prefix", field)
		}
	}
}

func TestPromptFingerprintIncludesTrailingRenderingFields(t *testing.T) {
	messages := `{"messages":[{"role":"user","content":"` + strings.Repeat("same prompt ", 600) + `"}]`
	for _, field := range []string{"tools", "tool_choice", "chat_template_kwargs", "model"} {
		a := fingerprintPrompt([]byte(messages + `,"` + field + `":"first"}`))
		b := fingerprintPrompt([]byte(messages + `,"` + field + `":"other"}`))
		if sharedBlocks(a, b) != 0 {
			t.Fatalf("changed trailing %s retained a warm prefix", field)
		}
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
