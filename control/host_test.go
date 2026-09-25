package control_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jaredt87/ackOS/control"
	"github.com/jaredt87/ackOS/integrations/memory"
	"github.com/jaredt87/ackOS/kernel"
)

func TestHostMemoryProviderLifecycle(t *testing.T) {
	resource, err := memory.NewResource("resource-a", "initial")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := memory.NewProvider(resource)
	if err != nil {
		t.Fatal(err)
	}
	host, err := control.NewHost(kernel.NewRuntime("initial", kernel.AllowPolicy{}), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Register("memory", provider); err != nil {
		t.Fatal(err)
	}

	result, err := host.Control(context.Background(), "memory", control.ControlRequest{
		Target:  control.ResourceRef{ID: "resource-a", Fingerprint: "initial"},
		Desired: control.ResourceRef{ID: "resource-a", Fingerprint: "running"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Authority.ExecutionID == "" || result.Execution.ExecutionID != result.Authority.ExecutionID {
		t.Fatal("execution was not bound to host-issued authority")
	}
	if result.Verification.Resource.Fingerprint != "running" {
		t.Fatalf("verification fingerprint = %q, want running", result.Verification.Resource.Fingerprint)
	}
	if got := result.Transition.Before; got != "initial" {
		t.Fatalf("transition before = %q", got)
	}
}

func TestHostRejectsResourceSubstitution(t *testing.T) {
	resource, _ := memory.NewResource("resource-a", "initial")
	provider, _ := memory.NewProvider(resource)
	host, _ := control.NewHost(kernel.NewRuntime("initial", kernel.AllowPolicy{}), time.Second)
	_ = host.Register("memory", provider)

	resource.Set("other")
	_, err := host.Control(context.Background(), "memory", control.ControlRequest{
		Target:  control.ResourceRef{ID: "resource-a", Fingerprint: "initial"},
		Desired: control.ResourceRef{ID: "resource-a", Fingerprint: "running"},
	})
	if err == nil {
		t.Fatal("control unexpectedly succeeded after resource mutation")
	}
}

func TestHostDoesNotInterpretFingerprint(t *testing.T) {
	resource, _ := memory.NewResource("resource-a", "opaque::provider-state")
	provider, _ := memory.NewProvider(resource)
	host, _ := control.NewHost(kernel.NewRuntime("opaque::provider-state", kernel.AllowPolicy{}), time.Second)
	_ = host.Register("memory", provider)

	_, err := host.Control(context.Background(), "memory", control.ControlRequest{
		Target:  control.ResourceRef{ID: "resource-a", Fingerprint: "opaque::provider-state"},
		Desired: control.ResourceRef{ID: "resource-a", Fingerprint: "another::provider-state"},
	})
	if err != nil {
		t.Fatal(err)
	}
}

type verificationProvider struct {
	base     control.Provider
	verifyFn func(context.Context, control.VerifyRequest) (control.Verification, error)
}

func (p verificationProvider) Observe(ctx context.Context, req control.ObserveRequest) (control.Observation, error) {
	return p.base.Observe(ctx, req)
}

func (p verificationProvider) Execute(ctx context.Context, req control.ExecuteRequest) (control.Execution, error) {
	return p.base.Execute(ctx, req)
}

func (p verificationProvider) Verify(ctx context.Context, req control.VerifyRequest) (control.Verification, error) {
	return p.verifyFn(ctx, req)
}

func newVerificationProvider(t *testing.T, verifyFn func(context.Context, control.VerifyRequest) (control.Verification, error)) control.Provider {
	t.Helper()
	resource, err := memory.NewResource("resource-a", "initial")
	if err != nil {
		t.Fatal(err)
	}
	base, err := memory.NewProvider(resource)
	if err != nil {
		t.Fatal(err)
	}
	return verificationProvider{base: base, verifyFn: verifyFn}
}

func controlRequest() control.ControlRequest {
	return control.ControlRequest{
		Target:  control.ResourceRef{ID: "resource-a", Fingerprint: "initial"},
		Desired: control.ResourceRef{ID: "resource-a", Fingerprint: "running"},
	}
}

func TestHostProviderVerificationFailureTransitionsToRecovery(t *testing.T) {
	expected := errors.New("verification failed")
	provider := newVerificationProvider(t, func(context.Context, control.VerifyRequest) (control.Verification, error) {
		return control.Verification{}, expected
	})
	runtime := kernel.NewRuntime("initial", kernel.AllowPolicy{})
	host, err := control.NewHost(runtime, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Register("test", provider); err != nil {
		t.Fatal(err)
	}

	_, err = host.Control(context.Background(), "test", controlRequest())
	if !errors.Is(err, kernel.ErrVerificationFailed) {
		t.Fatalf("error = %v, want ErrVerificationFailed", err)
	}
	if got := runtime.Phase(); got != kernel.PhaseRecovery {
		t.Fatalf("runtime phase = %s, want %s", got, kernel.PhaseRecovery)
	}
}

func TestHostProviderVerificationTimeoutTransitionsToRecovery(t *testing.T) {
	provider := newVerificationProvider(t, func(ctx context.Context, _ control.VerifyRequest) (control.Verification, error) {
		<-ctx.Done()
		return control.Verification{}, ctx.Err()
	})
	runtime := kernel.NewRuntime("initial", kernel.AllowPolicy{})
	host, err := control.NewHost(runtime, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Register("test", provider); err != nil {
		t.Fatal(err)
	}

	_, err = host.Control(context.Background(), "test", controlRequest())
	if !errors.Is(err, kernel.ErrVerificationFailed) {
		t.Fatalf("error = %v, want ErrVerificationFailed", err)
	}
	if got := runtime.Phase(); got != kernel.PhaseRecovery {
		t.Fatalf("runtime phase = %s, want %s", got, kernel.PhaseRecovery)
	}
}
