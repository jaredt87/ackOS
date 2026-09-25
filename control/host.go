package control

import (
	"context"
	"fmt"
	"time"

	"github.com/jaredt87/ackOS/kernel"
)

const defaultInvocationTimeout = 30 * time.Second

// Host is the small in-process boundary between the kernel and providers.
// Runtime owns authorization and lifecycle state; Host owns provider routing
// and the invocation boundary. Providers own only domain-specific resource state.
type Host struct {
	runtime   *kernel.Runtime
	providers map[string]Provider
	timeout   time.Duration
	gates     providerGates
}

type ControlRequest struct {
	Target       ResourceRef
	Desired      ResourceRef
	AuthorityTTL time.Duration
}

type ControlResult struct {
	Observation  Observation
	Transition   kernel.Transition
	Authority    kernel.Authority
	Execution    Execution
	Verification Verification
}

func NewHost(runtime *kernel.Runtime, timeout time.Duration) (*Host, error) {
	if runtime == nil {
		return nil, fmt.Errorf("runtime is required")
	}
	if timeout <= 0 {
		timeout = defaultInvocationTimeout
	}
	return &Host{runtime: runtime, providers: make(map[string]Provider), timeout: timeout}, nil
}

func (h *Host) Register(name string, provider Provider) error {
	if name == "" {
		return fmt.Errorf("provider name is required")
	}
	if provider == nil {
		return fmt.Errorf("provider is required")
	}
	if _, exists := h.providers[name]; exists {
		return fmt.Errorf("provider %q already registered", name)
	}
	h.providers[name] = provider
	return nil
}

func (h *Host) provider(name string) (Provider, error) {
	p, ok := h.providers[name]
	if !ok {
		return nil, fmt.Errorf("provider %q is not registered", name)
	}
	return p, nil
}

func (h *Host) Control(ctx context.Context, providerName string, req ControlRequest) (ControlResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !req.Target.valid() || !req.Desired.valid() || req.Target.ID != req.Desired.ID {
		return ControlResult{}, fmt.Errorf("target and desired resource references must identify the same non-empty resource")
	}
	p, err := h.provider(providerName)
	if err != nil {
		return ControlResult{}, err
	}
	if err := h.runtime.AcquireLifecycle(ctx); err != nil {
		return ControlResult{}, err
	}
	defer h.runtime.ReleaseLifecycle()

	observeCtx, cancel := context.WithTimeout(ctx, h.timeout)
	var observed Observation
	err = h.callProvider(observeCtx, providerName, "observe", func() error {
		var callErr error
		observed, callErr = p.Observe(observeCtx, ObserveRequest{Target: req.Target})
		return callErr
	})
	cancel()
	if err != nil {
		return ControlResult{}, fmt.Errorf("observe: %w", err)
	}
	if observed.Resource.ID != req.Target.ID || observed.Resource.Fingerprint == "" {
		return ControlResult{}, fmt.Errorf("provider returned invalid observation")
	}

	kernelObservation, err := kernel.NewObservation(observed.Resource.ID, observed.Resource.Fingerprint, 0, observed.ObservedAt)
	if err != nil {
		return ControlResult{}, err
	}
	if phase := h.runtime.Phase(); phase == kernel.PhaseRecovery {
		recoverySubject, ok := h.runtime.RecoverySubject()
		if !ok || observed.Resource.ID != recoverySubject {
			return ControlResult{}, fmt.Errorf("provider observation does not match recovery subject")
		}
		// RecoverySubject is the kernel's generic lifecycle subject; provider identity
		// beyond that subject is domain-specific and is not interpreted by the Host.
		if err := h.runtime.Recover(kernelObservation); err != nil {
			return ControlResult{}, err
		}
	} else if err := h.runtime.Observe(kernelObservation); err != nil {
		return ControlResult{}, err
	}
	if _, err := h.runtime.Normalize(kernel.Proposal{Subject: req.Desired.ID, TargetState: req.Desired.Fingerprint}); err != nil {
		return ControlResult{}, err
	}
	transition, err := h.runtime.Reconcile()
	if err != nil {
		return ControlResult{}, err
	}
	if _, err := h.runtime.Govern(); err != nil {
		return ControlResult{}, err
	}
	authority, err := h.runtime.Reserve(req.AuthorityTTL)
	if err != nil {
		return ControlResult{}, err
	}

	execution := Execution{}
	execCtx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()
	executionResult, err := h.runtime.Start(execCtx, providerExecutor{
		host:         h,
		providerName: providerName,
		provider:     p,
		execution: &execution,
		request: ExecuteRequest{
			ExecutionID: authority.ExecutionID,
			Target:      req.Target,
			Payload:     []byte(req.Desired.Fingerprint),
		},
	})
	if err != nil {
		return ControlResult{Observation: observed, Transition: transition, Authority: authority}, err
	}
	authority.Consumed = true
	if !executionResult.Success {
		return ControlResult{Observation: observed, Transition: transition, Authority: authority, Execution: execution}, fmt.Errorf("execution failed: %s", executionResult.Message)
	}

	verification := Verification{}
	verifyBase := context.WithoutCancel(ctx)
	verifyCtx, cancel := context.WithTimeout(verifyBase, h.timeout)
	defer cancel()
	if err := h.runtime.Verify(verifyCtx, providerVerifier{
		host:         h,
		providerName: providerName,
		provider:     p,
		verification: &verification,
		request: VerifyRequest{
			ExecutionID: authority.ExecutionID,
			Expected:    req.Desired,
		},
	}); err != nil {
		return ControlResult{Observation: observed, Transition: transition, Authority: authority, Execution: execution, Verification: verification}, err
	}
	if err := h.runtime.Commit(); err != nil {
		return ControlResult{Observation: observed, Transition: transition, Authority: authority, Execution: execution, Verification: verification}, err
	}
	return ControlResult{
		Observation:  observed,
		Transition:   transition,
		Authority:    authority,
		Execution:    execution,
		Verification: verification,
	}, nil
}

func (h *Host) execute(ctx context.Context, providerName string, p Provider, req ExecuteRequest) (Execution, error) {
	if err := ctx.Err(); err != nil {
		return Execution{}, err
	}
	var result Execution
	err := h.callProvider(ctx, providerName, req.ExecutionID, func() error {
		var callErr error
		result, callErr = p.Execute(ctx, req)
		return callErr
	})
	if err != nil {
		return Execution{}, err
	}
	if result.ExecutionID != req.ExecutionID {
		return Execution{}, fmt.Errorf("provider returned mismatched execution ID")
	}
	return result, nil
}

func (h *Host) verify(ctx context.Context, providerName string, p Provider, req VerifyRequest) (Verification, error) {
	if err := ctx.Err(); err != nil {
		return Verification{}, err
	}
	var result Verification
	err := h.callProvider(ctx, providerName, req.ExecutionID, func() error {
		var callErr error
		result, callErr = p.Verify(ctx, req)
		return callErr
	})
	if err != nil {
		return Verification{}, err
	}
	if result.Resource.ID != req.Expected.ID || result.Resource.Fingerprint != req.Expected.Fingerprint {
		return Verification{}, fmt.Errorf("provider verification does not match expected resource")
	}
	if result.VerifiedAt.IsZero() {
		return Verification{}, fmt.Errorf("provider verification timestamp is required")
	}
	return result, nil
}

type providerExecutor struct {
	host      *Host
	providerName string
	provider  Provider
	execution *Execution
	request   ExecuteRequest
}

func (e providerExecutor) Execute(ctx context.Context, _ kernel.Transition, _ kernel.Authority) kernel.ExecutionResult {
	result, err := e.host.execute(ctx, e.providerName, e.provider, e.request)
	if err != nil {
		return kernel.ExecutionResult{Message: err.Error()}
	}
	*e.execution = result
	return kernel.ExecutionResult{Success: true, Message: "provider execution completed"}
}

type providerVerifier struct {
	host         *Host
	providerName string
	provider     Provider
	verification *Verification
	request      VerifyRequest
}

func (v providerVerifier) Verify(ctx context.Context, _ kernel.Transition, _ kernel.Authority) (kernel.Observation, error) {
	result, err := v.host.verify(ctx, v.providerName, v.provider, v.request)
	if err != nil {
		return kernel.Observation{}, err
	}
	*v.verification = result
	return kernel.NewObservation(
		result.Resource.ID,
		result.Resource.Fingerprint,
		0,
		result.VerifiedAt,
	)
}
