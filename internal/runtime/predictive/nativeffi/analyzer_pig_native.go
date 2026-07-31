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
	nativeABIVersion   = 1
	nativeErrorBytes   = 512
	cacheDigestBytes   = 32
	maximumCGoByteCopy = math.MaxInt32
)

type Analyzer struct {
	mu                         sync.RWMutex
	handle                     *C.PigTokenizerHandle
	manifestID                 string
	backendEpoch               string
	blockSize                  int
	completionAddSpecialTokens bool
	chatAddSpecialTokens       bool
}

type nativeError struct {
	operation string
	code      int
	message   string
}

func Open(config Config) (*Analyzer, error) {
	if config.TokenizerPath == "" {
		return nil, fmt.Errorf("native tokenizer path is required")
	}
	if config.ManifestID == "" {
		return nil, fmt.Errorf("native tokenizer manifest id is required")
	}
	if config.BackendEpoch == "" {
		return nil, fmt.Errorf("native tokenizer backend epoch is required")
	}
	if config.BlockSize <= 0 {
		return nil, fmt.Errorf("native tokenizer block size must be positive")
	}
	if len(config.Key) != cacheDigestBytes {
		return nil, fmt.Errorf("native tokenizer key must contain exactly %d bytes", cacheDigestBytes)
	}
	if version := uint32(C.pig_tokenizer_abi_version()); version != nativeABIVersion {
		return nil, fmt.Errorf("native tokenizer ABI version = %d, want %d", version, nativeABIVersion)
	}

	tokenizerPath := C.CString(config.TokenizerPath)
	defer C.free(unsafe.Pointer(tokenizerPath))
	manifestID := C.CString(config.ManifestID)
	defer C.free(unsafe.Pointer(manifestID))
	backendEpoch := C.CString(config.BackendEpoch)
	defer C.free(unsafe.Pointer(backendEpoch))
	var errorBuffer [nativeErrorBytes]C.char
	var handle *C.PigTokenizerHandle
	status := C.pig_tokenizer_open(
		tokenizerPath,
		manifestID,
		backendEpoch,
		C.uint64_t(config.BlockSize),
		(*C.uint8_t)(unsafe.Pointer(unsafe.SliceData(config.Key))),
		C.size_t(len(config.Key)),
		&handle,
		&errorBuffer[0],
		C.size_t(len(errorBuffer)),
	)
	runtime.KeepAlive(config.Key)
	if status != C.PIG_TOKENIZER_OK {
		return nil, newNativeError("open", status, &errorBuffer[0])
	}
	if handle == nil {
		return nil, fmt.Errorf("native tokenizer open returned a nil handle")
	}
	return &Analyzer{
		handle:                     handle,
		manifestID:                 config.ManifestID,
		backendEpoch:               config.BackendEpoch,
		blockSize:                  config.BlockSize,
		completionAddSpecialTokens: config.CompletionAddSpecialTokens,
		chatAddSpecialTokens:       config.ChatAddSpecialTokens,
	}, nil
}

func (a *Analyzer) Analyze(ctx context.Context, class runtimepredictive.RequestClass, rendered []byte, _ runtimepredictive.RequestFeatures) (result runtimepredictive.TokenBlockAnalysis, err error) {
	if a == nil {
		return runtimepredictive.TokenBlockAnalysis{}, fmt.Errorf("%w: analyzer is nil", ErrUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return runtimepredictive.TokenBlockAnalysis{}, err
	}
	addSpecialTokens, err := a.specialTokenPolicy(class)
	if err != nil {
		return runtimepredictive.TokenBlockAnalysis{}, err
	}

	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.handle == nil {
		return runtimepredictive.TokenBlockAnalysis{}, fmt.Errorf("%w: analyzer is closed", ErrUnavailable)
	}
	var input *C.uint8_t
	if len(rendered) > 0 {
		input = (*C.uint8_t)(unsafe.Pointer(unsafe.SliceData(rendered)))
	}
	var errorBuffer [nativeErrorBytes]C.char
	var analysisHandle *C.PigTokenizationAnalysisHandle
	status := C.pig_tokenizer_analyze(
		a.handle,
		input,
		C.size_t(len(rendered)),
		boolByte(addSpecialTokens),
		&analysisHandle,
		&errorBuffer[0],
		C.size_t(len(errorBuffer)),
	)
	runtime.KeepAlive(rendered)
	if status != C.PIG_TOKENIZER_OK {
		return runtimepredictive.TokenBlockAnalysis{}, newNativeError("analyze", status, &errorBuffer[0])
	}
	if analysisHandle == nil {
		return runtimepredictive.TokenBlockAnalysis{}, fmt.Errorf("native tokenizer analyze returned a nil handle")
	}
	defer func() {
		if analysisHandle == nil {
			return
		}
		if status := C.pig_tokenizer_analysis_destroy(&analysisHandle); status != C.PIG_TOKENIZER_OK && err == nil {
			err = newNativeError("destroy analysis", status, nil)
			result = runtimepredictive.TokenBlockAnalysis{}
		}
	}()

	var view C.PigTokenizationAnalysisView
	if status := C.pig_tokenizer_analysis_view(analysisHandle, &view); status != C.PIG_TOKENIZER_OK {
		return runtimepredictive.TokenBlockAnalysis{}, newNativeError("view analysis", status, nil)
	}
	if uint64(view.token_count) > math.MaxInt64 {
		return runtimepredictive.TokenBlockAnalysis{}, fmt.Errorf("native tokenizer token count exceeds Go range")
	}
	if uint64(view.full_block_count) > uint64(maximumCGoByteCopy/cacheDigestBytes) {
		return runtimepredictive.TokenBlockAnalysis{}, fmt.Errorf("native tokenizer digest result exceeds Go copy range")
	}
	fullBlockCount := int(view.full_block_count)
	digestByteCount := fullBlockCount * cacheDigestBytes
	if digestByteCount > 0 && view.full_block_digests == nil {
		return runtimepredictive.TokenBlockAnalysis{}, fmt.Errorf("native tokenizer returned nil full-block digests")
	}
	digestBytes := C.GoBytes(unsafe.Pointer(view.full_block_digests), C.int(digestByteCount))
	fullBlockDigests := make([]runtimepredictive.CacheBlockDigest, fullBlockCount)
	for index := range fullBlockDigests {
		copy(fullBlockDigests[index][:], digestBytes[index*cacheDigestBytes:(index+1)*cacheDigestBytes])
	}
	if uint64(view.partial_block_tokens) > math.MaxInt64 {
		return runtimepredictive.TokenBlockAnalysis{}, fmt.Errorf("native tokenizer partial-block count exceeds Go range")
	}
	var partialBlockDigest runtimepredictive.CacheBlockDigest
	for index := range partialBlockDigest {
		partialBlockDigest[index] = byte(view.partial_block_digest[index])
	}
	result = runtimepredictive.TokenBlockAnalysis{
		ManifestID:         a.manifestID,
		BackendEpoch:       a.backendEpoch,
		BlockSize:          a.blockSize,
		ExactInputTokens:   int64(view.token_count),
		FullBlockDigests:   fullBlockDigests,
		PartialBlockTokens: int64(view.partial_block_tokens),
		PartialBlockDigest: partialBlockDigest,
	}
	if err := result.Validate(runtimepredictive.CacheMirrorEpoch{
		ManifestID:   a.manifestID,
		BackendEpoch: a.backendEpoch,
		BlockSize:    a.blockSize,
	}); err != nil {
		return runtimepredictive.TokenBlockAnalysis{}, fmt.Errorf("validate native tokenizer analysis: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return runtimepredictive.TokenBlockAnalysis{}, err
	}
	return result, nil
}

func (a *Analyzer) Close() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.handle == nil {
		return nil
	}
	if status := C.pig_tokenizer_destroy(&a.handle); status != C.PIG_TOKENIZER_OK {
		return newNativeError("close", status, nil)
	}
	return nil
}

func (a *Analyzer) specialTokenPolicy(class runtimepredictive.RequestClass) (bool, error) {
	switch class {
	case runtimepredictive.RequestClassCompletion:
		return a.completionAddSpecialTokens, nil
	case runtimepredictive.RequestClassChat:
		return a.chatAddSpecialTokens, nil
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
