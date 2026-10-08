package control

import (
	"context"
	"fmt"

	"github.com/jaredt87/ackOS/kernel"
)

// NewProviderExecutor returns a Kernel executor backed by a control provider.
// The adapter carries the request and execution result for one lifecycle.
// If an executionError pointer is supplied, execution-phase errors are captured
// there without changing the existing Kernel result contract.
func NewProviderExecutor(provider Provider, request ExecuteRequest, execution *Execution, executionError ...*error) kernel.Executor {
	return providerExecutor{
		provider:       provider,
		request:        request,
		execution:      execution,
		executionError: optionalExecutionError(executionError),
	}
}

type providerExecutor struct {
	provider       Provider
	execution      *Execution
	executionError *error
	request        ExecuteRequest
}

func optionalExecutionError(errors []*error) *error {
	if len(errors) == 0 {
		return nil
	}
	return errors[0]
}

func (e providerExecutor) captureExecutionError(err error) {
	if e.executionError != nil {
		*e.executionError = err
	}
}

func (e providerExecutor) Execute(ctx context.Context, _ kernel.Transition, _ kernel.Authority, before kernel.Observation) kernel.ExecutionResult {
	if err := ctx.Err(); err != nil {
		e.captureExecutionError(err)
		return kernel.ExecutionResult{Message: err.Error()}
	}
	request := e.request
	request.Before = beforeObservation(before)
	result, err := e.provider.Execute(ctx, request)
	if err != nil {
		e.captureExecutionError(err)
		return kernel.ExecutionResult{Message: err.Error()}
	}
	if result.ExecutionID != request.ExecutionID {
		err := fmt.Errorf("provider returned mismatched execution ID")
		e.captureExecutionError(err)
		return kernel.ExecutionResult{Message: err.Error()}
	}
	if e.execution != nil {
		*e.execution = result
	}
	return kernel.ExecutionResult{
		ExecutionID: result.ExecutionID,
		Success:     true,
		Message:     "provider execution completed",
		Evidence:    append([]byte(nil), result.Evidence...),
	}
}

// NewProviderVerifier returns a Kernel verifier backed by a control provider.
// The adapter carries the expected request and verification result for one lifecycle.
func NewProviderVerifier(provider Provider, request VerifyRequest, verification *Verification) kernel.Verifier {
	return providerVerifier{provider: provider, verification: verification, request: request}
}

type providerVerifier struct {
	provider     Provider
	verification *Verification
	request      VerifyRequest
}

func (v providerVerifier) Verify(ctx context.Context, _ kernel.Transition, authority kernel.Authority, before kernel.Observation, execution kernel.ExecutionResult) (kernel.Observation, error) {
	if err := ctx.Err(); err != nil {
		return kernel.Observation{}, err
	}
	request := v.request
	request.ExecutionID = authority.ExecutionID
	request.Before = beforeObservation(before)
	request.Execution = Execution{
		ExecutionID: execution.ExecutionID,
		Evidence:    append([]byte(nil), execution.Evidence...),
	}
	result, err := v.provider.Verify(ctx, request)
	if err != nil {
		return kernel.Observation{}, err
	}
	if result.Resource.ID != request.Expected.ID || result.Resource.Fingerprint != request.Expected.Fingerprint {
		return kernel.Observation{}, fmt.Errorf("provider verification does not match expected resource")
	}
	if result.VerifiedAt.IsZero() {
		return kernel.Observation{}, fmt.Errorf("provider verification timestamp is required")
	}
	if v.verification != nil {
		*v.verification = result
	}
	return kernel.NewObservation(
		result.Resource.ID,
		result.Resource.Fingerprint,
		result.Version,
		result.VerifiedAt,
	)
}

func beforeObservation(before kernel.Observation) Observation {
	return Observation{
		Resource:   ResourceRef{ID: before.Subject, Fingerprint: before.State},
		Version:    before.Version,
		ObservedAt: before.ObservedAt,
	}
}
