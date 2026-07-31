//go:build pig_native && cgo

package nativeffi

/*
#cgo CFLAGS: -I${SRCDIR}/../../../../native/tokenizer/include
#cgo LDFLAGS: -lpig_tokenizer_native
#include <stdlib.h>
#include "pig_tokenizer.h"
*/
import "C"

import (
	"context"
	"fmt"
	"math"
	"runtime"
	"sync"
	"unsafe"

	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

const (
	nativeABIVersion = 3
	nativeErrorBytes = 512
)

type nativeError struct {
	operation string
	code      int
	message   string
}

type Counter struct {
	mu                         sync.RWMutex
	handle                     *C.PigTokenizerCountHandle
	manifestID                 string
	backendEpoch               string
	completionAddSpecialTokens bool
	chatAddSpecialTokens       bool
}

func OpenCounter(config CounterConfig) (*Counter, error) {
	if config.TokenizerPath == "" {
		return nil, fmt.Errorf("native counter tokenizer path is required")
	}
	if config.ManifestID == "" {
		return nil, fmt.Errorf("native counter manifest id is required")
	}
	if config.BackendEpoch == "" {
		return nil, fmt.Errorf("native counter backend epoch is required")
	}
	if version := uint32(C.pig_tokenizer_abi_version()); version != nativeABIVersion {
		return nil, fmt.Errorf("native tokenizer ABI version = %d, want %d", version, nativeABIVersion)
	}

	tokenizerPath := C.CString(config.TokenizerPath)
	defer C.free(unsafe.Pointer(tokenizerPath))
	var errorBuffer [nativeErrorBytes]C.char
	var handle *C.PigTokenizerCountHandle
	status := C.pig_tokenizer_count_open(
		tokenizerPath,
		&handle,
		&errorBuffer[0],
		C.size_t(len(errorBuffer)),
	)
	if status != C.PIG_TOKENIZER_OK {
		return nil, newNativeError("open counter", status, &errorBuffer[0])
	}
	if handle == nil {
		return nil, fmt.Errorf("native counter open returned a nil handle")
	}
	return &Counter{
		handle:                     handle,
		manifestID:                 config.ManifestID,
		backendEpoch:               config.BackendEpoch,
		completionAddSpecialTokens: config.CompletionAddSpecialTokens,
		chatAddSpecialTokens:       config.ChatAddSpecialTokens,
	}, nil
}

func (c *Counter) Count(ctx context.Context, class runtimepredictive.RequestClass, rendered []byte) (runtimepredictive.TokenCountAnalysis, error) {
	if c == nil {
		return runtimepredictive.TokenCountAnalysis{}, fmt.Errorf("%w: counter is nil", ErrUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return runtimepredictive.TokenCountAnalysis{}, err
	}
	addSpecialTokens, err := c.specialTokenPolicy(class)
	if err != nil {
		return runtimepredictive.TokenCountAnalysis{}, err
	}

	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.handle == nil {
		return runtimepredictive.TokenCountAnalysis{}, fmt.Errorf("%w: counter is closed", ErrUnavailable)
	}
	var input *C.uint8_t
	if len(rendered) > 0 {
		input = (*C.uint8_t)(unsafe.Pointer(unsafe.SliceData(rendered)))
	}
	var errorBuffer [nativeErrorBytes]C.char
	var tokenCount C.uint64_t
	status := C.pig_tokenizer_count(
		c.handle,
		input,
		C.size_t(len(rendered)),
		boolByte(addSpecialTokens),
		&tokenCount,
		&errorBuffer[0],
		C.size_t(len(errorBuffer)),
	)
	runtime.KeepAlive(rendered)
	if status != C.PIG_TOKENIZER_OK {
		return runtimepredictive.TokenCountAnalysis{}, newNativeError("count", status, &errorBuffer[0])
	}
	if uint64(tokenCount) > math.MaxInt64 {
		return runtimepredictive.TokenCountAnalysis{}, fmt.Errorf("native tokenizer token count exceeds Go range")
	}
	result := runtimepredictive.TokenCountAnalysis{
		ManifestID:       c.manifestID,
		BackendEpoch:     c.backendEpoch,
		ExactInputTokens: int64(tokenCount),
	}
	if err := result.Validate(c.manifestID, c.backendEpoch); err != nil {
		return runtimepredictive.TokenCountAnalysis{}, fmt.Errorf("validate native token count: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return runtimepredictive.TokenCountAnalysis{}, err
	}
	return result, nil
}

func (c *Counter) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.handle == nil {
		return nil
	}
	if status := C.pig_tokenizer_count_destroy(&c.handle); status != C.PIG_TOKENIZER_OK {
		return newNativeError("close counter", status, nil)
	}
	return nil
}

func (c *Counter) specialTokenPolicy(class runtimepredictive.RequestClass) (bool, error) {
	switch class {
	case runtimepredictive.RequestClassCompletion:
		return c.completionAddSpecialTokens, nil
	case runtimepredictive.RequestClassChat:
		return c.chatAddSpecialTokens, nil
	default:
		return false, fmt.Errorf("%w: %q", runtimepredictive.ErrUnsupportedRequestClass, class)
	}
}

func newNativeError(operation string, status C.int32_t, message *C.char) error {
	text := ""
	if message != nil {
		text = C.GoString(message)
	}
	return &nativeError{operation: operation, code: int(status), message: text}
}

func (e *nativeError) Error() string {
	if e.message == "" {
		return fmt.Sprintf("native tokenizer %s failed with status %d", e.operation, e.code)
	}
	return fmt.Sprintf("native tokenizer %s failed with status %d: %s", e.operation, e.code, e.message)
}

func boolByte(value bool) C.uint8_t {
	if value {
		return 1
	}
	return 0
}
