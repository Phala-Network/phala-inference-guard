package request

import "bytes"

// PromptBlockBytes is the granularity at which Prompt prefixes are compared.
// A block is about 250 tokens of English text, small next to the long
// conversation prefixes that dominate Prefill cost.
const PromptBlockBytes = 1024

// PromptFingerprint identifies the Prompt prefix of a request body. Block i
// hashes every byte from the start of the Prompt through the end of block i,
// so two requests share their first n blocks exactly when their first n·1 KiB
// of Prompt bytes are identical. Bytes is the Prompt length in body bytes.
type PromptFingerprint struct {
	Blocks []uint64
	Bytes  int64
}

// promptFields are the top-level request fields that carry the Prompt, in the
// order they are searched. Hashing starts at the field so that sampling
// options serialized before it (max_tokens, temperature) do not break prefix
// matches between turns of one conversation.
var promptFields = [][]byte{[]byte(`"messages"`), []byte(`"input"`), []byte(`"prompt"`)}

func fingerprintPrompt(body []byte) PromptFingerprint {
	start := 0
	for _, field := range promptFields {
		if i := bytes.Index(body, field); i >= 0 {
			start = i
			break
		}
	}
	prompt := body[start:]
	if len(prompt) == 0 {
		return PromptFingerprint{}
	}
	blocks := make([]uint64, 0, (len(prompt)+PromptBlockBytes-1)/PromptBlockBytes)
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	hash := uint64(offset64)
	for i, b := range prompt {
		hash ^= uint64(b)
		hash *= prime64
		if (i+1)%PromptBlockBytes == 0 || i == len(prompt)-1 {
			blocks = append(blocks, hash)
		}
	}
	return PromptFingerprint{Blocks: blocks, Bytes: int64(len(prompt))}
}
