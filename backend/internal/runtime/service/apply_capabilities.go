package service

import (
	"context"
	"errors"
	"fmt"
)

type CapabilityFailure struct {
	Service ServiceName `json:"service"`
	Cause   error       `json:"-"`
	Reason  string      `json:"reason"`
}

func (failure CapabilityFailure) Error() string {
	reason := failure.Reason
	if failure.Cause != nil {
		reason = failure.Cause.Error()
	}
	return fmt.Sprintf("%s apply failed: %s", failure.Service, reason)
}

func (failure CapabilityFailure) Unwrap() error {
	return failure.Cause
}

type ApplyReport struct {
	AppliedArtifacts []RenderedArtifact
	Failures         []CapabilityFailure
}

func (report ApplyReport) Err() error {
	errorsByCapability := make([]error, 0, len(report.Failures))
	for _, failure := range report.Failures {
		errorsByCapability = append(errorsByCapability, failure)
	}
	return errors.Join(errorsByCapability...)
}

func (runtime Runtime) ApplyCapabilities(ctx context.Context, artifacts []RenderedArtifact) ApplyReport {
	return runtime.applyCapabilities(ctx, artifacts)
}

// ApplyCapabilitiesForTransaction carries the transaction identity into the
// service apply phase so status reads cannot race and rebind a fresh receipt
// to an older runtime transaction.
func (runtime Runtime) ApplyCapabilitiesForTransaction(ctx context.Context, transactionID string, artifacts []RenderedArtifact) ApplyReport {
	return runtime.applyCapabilities(withTransactionID(ctx, transactionID), artifacts)
}

func (runtime Runtime) applyCapabilities(ctx context.Context, artifacts []RenderedArtifact) ApplyReport {
	byService := groupByService(artifacts)
	report := ApplyReport{AppliedArtifacts: make([]RenderedArtifact, 0, len(artifacts))}
	for _, service := range serviceOrder() {
		items := byService[service]
		if len(items) == 0 {
			continue
		}
		if err := runtime.Controller.ReloadOrRestart(ctx, service, items); err != nil {
			report.Failures = append(report.Failures, CapabilityFailure{Service: service, Cause: err, Reason: err.Error()})
			continue
		}
		report.AppliedArtifacts = append(report.AppliedArtifacts, items...)
	}
	return report
}
