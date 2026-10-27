package request

import "encoding/json"

// PromptBlockBytes is the granularity at which Prompt prefixes are compared.
// A block is about 250 tokens of English text, small next to the long
// conversation prefixes that dominate Prefill cost.
const PromptBlockBytes = 1024

// PromptFingerprint identifies the Prompt prefix of a request body. Block i
// hashes every byte from the start of the Prompt through the end of block i,
// so two requests share their first n blocks exactly when their first n·1 KiB
// of request bytes are identical. Bytes is the request length in body bytes.
// Every block is seeded by all non-Prompt fields, so rendering inputs such as
// tools and model cannot inherit a different context's warm prefix regardless
// of their serialized position. Sampling changes may conservatively miss.
type PromptFingerprint struct {
	Blocks []uint64
	Bytes  int64
}

func fingerprintPrompt(body []byte) PromptFingerprint {
	// This input already fits the scanner's bounded body budget. Keep every
	// non-Prompt field in the context rather than guessing which extensions
	// the backend might render ahead of the Prompt.
	var context map[string]json.RawMessage
	if err := json.Unmarshal(body, &context); err != nil || context == nil {
		return PromptFingerprint{}
	}
	delete(context, "messages")
	delete(context, "input")
	delete(context, "prompt")
	contextBytes, err := json.Marshal(context)
	if err != nil {
		return PromptFingerprint{}
	}
	prompt := body
	if len(prompt) == 0 {
		return PromptFingerprint{}
	}
	blocks := make([]uint64, 0, (len(prompt)+PromptBlockBytes-1)/PromptBlockBytes)
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	hash := uint64(offset64)
	for _, b := range contextBytes {
		hash ^= uint64(b)
		hash *= prime64
	}
	for i, b := range prompt {
		hash ^= uint64(b)
		hash *= prime64
		if (i+1)%PromptBlockBytes == 0 || i == len(prompt)-1 {
			blocks = append(blocks, hash)
		}
	}
	return PromptFingerprint{Blocks: blocks, Bytes: int64(len(prompt))}
}
