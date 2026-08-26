package server

import (
	"context"
	"fmt"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
	apprequest "github.com/Phala-Network/phala-inference-guard/internal/app/request"
)

func tpsDemandForClassification(classification apprequest.Classification) coreadmission.TPSRequestDemand {
	if classification.Supported {
		return coreadmission.NewTPSRequestDemand(classification.DecodeSequences)
	}
	if classification.SingleSequenceFallback {
		return coreadmission.NewFallbackTPSRequestDemand()
	}
	return coreadmission.TPSRequestDemand{}
}

func (s *proxyServer) decideAdmission(
	ctx context.Context,
	demand coreadmission.TPSRequestDemand,
) (result admissionDecision) {
	if s == nil || s.admission == nil {
		return unavailableAdmissionDecision(demand)
	}
	defer func() {
		if recover() != nil {
			s.admissionFailures.decide.Add(1)
			result = unavailableAdmissionDecision(demand)
		}
	}()
	result = s.admission.Decide(ctx, demand)
	if !result.valid() {
		s.admissionFailures.decide.Add(1)
		return unavailableAdmissionDecision(demand)
	}
	return result
}

func unavailableAdmissionDecision(demand coreadmission.TPSRequestDemand) admissionDecision {
	return admissionDecision{Record: coreadmission.DecisionRecord{
		Action: coreadmission.ActionProtect,
		Reason: coreadmission.ReasonControllerUnavailable,
		Scope:  coreadmission.ProtectionAvailability,
		Demand: demand,
	}}
}

func (s *proxyServer) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		defer func() {
			if recover() != nil {
				s.admissionFailures.close.Add(1)
				s.closeErr = fmt.Errorf("admission service close panicked")
			}
		}()
		if s.admission != nil {
			s.closeErr = s.admission.Close()
		}
	})
	return s.closeErr
}
