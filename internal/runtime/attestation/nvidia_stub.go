//go:build !linux || !cgo

package attestation

import (
	"context"
	"fmt"
)

type unavailableNVIDIACollector struct{}

func newNativeNVIDIACollector() nvidiaCollector {
	return unavailableNVIDIACollector{}
}

func (unavailableNVIDIACollector) Collect(context.Context, string, string) (string, error) {
	return "", fmt.Errorf("native NVIDIA collector requires linux with cgo and NVML")
}
