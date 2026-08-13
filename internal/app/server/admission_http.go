package server

import (
	"context"
	"fmt"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

func (s *proxyServer) decideAdmission(
	ctx context.Context,
	estimate domainpredictive.RequestEstimate,
) (result admissionDecision) {
	if s == nil || s.admission == nil {
		return unavailableAdmissionDecision(estimate)
	}
	defer func() {
		if recover() != nil {
			s.admissionFailures.decide.Add(1)
			result = unavailableAdmissionDecision(estimate)
		}
	}()
	result = s.admission.Decide(ctx, estimate)
	if !result.valid() {
		s.admissionFailures.decide.Add(1)
		return unavailableAdmissionDecision(estimate)
	}
	return result
}

func unavailableAdmissionDecision(estimate domainpredictive.RequestEstimate) admissionDecision {
	return admissionDecision{Record: coreadmission.DecisionRecord{
		Action:   coreadmission.ActionProtect,
		Reason:   coreadmission.ReasonControllerUnavailable,
		Scope:    coreadmission.ProtectionAvailability,
		Estimate: estimate,
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
