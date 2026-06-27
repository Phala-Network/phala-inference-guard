package request

import (
	"bytes"
	"errors"
)

const (
	PriorityModeAll         = "all"
	PriorityModePremiumOnly = "premium_only"

	PriorityRewriteStrategyFieldScan  = "field_scan"
	PriorityRewriteStrategyAppendLast = "append_last"
)

var ErrPriorityBodyNotObject = errors.New("priority body must be a JSON object")

type JSONRewriteOptions struct {
	InjectPriority      bool
	PriorityStrategy    string
	PriorityField       string
	PriorityValue       int
	StripEmptyToolCalls bool
}

func ShouldInjectBackendPriority(mode string, tier Tier) bool {
	switch mode {
	case PriorityModePremiumOnly:
		return tier == Premium
	default:
		return true
	}
}

func BackendPriorityValue(tier Tier, premiumValue, basicValue int) int {
	if tier == Premium {
		return premiumValue
	}
	return basicValue
}

func RewriteJSONPriority(body []byte, field string, value int) ([]byte, error) {
	return RewriteJSONPrioritySize(body, field, value, priorityStreamBufferSize)
}

func RewriteJSONPrioritySize(body []byte, field string, value int, bufferSize int) ([]byte, error) {
	return RewriteJSONBodySize(body, JSONRewriteOptions{
		InjectPriority:   true,
		PriorityStrategy: PriorityRewriteStrategyFieldScan,
		PriorityField:    field,
		PriorityValue:    value,
	}, bufferSize)
}

func RewriteJSONBodySize(body []byte, options JSONRewriteOptions, bufferSize int) ([]byte, error) {
	if options.InjectPriority && options.PriorityField == "" {
		return nil, errors.New("priority field must not be empty")
	}
	var out bytes.Buffer
	out.Grow(len(body) + len(options.PriorityField) + 32)
	reader := acquirePriorityReader(bytes.NewReader(body), bufferSize)
	err := rewriteJSONBodyStreamWithBuffer(reader, &out, options, bufferSize)
	releasePriorityReader(reader, bufferSize)
	if err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
