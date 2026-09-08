package mcp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jaredt87/ackOS/kernel"
)

type testExecutor struct {
	calls   int
	success bool
}

func (e *testExecutor) Execute(context.Context, kernel.Transition, kernel.Authority) kernel.ExecutionResult {
	e.calls++
	if !e.success {
		return kernel.ExecutionResult{Success: false, Message: "executor rejected transition"}
	}
	return kernel.ExecutionResult{Success: true, Message: "executed"}
}

type testVerifier struct {
	calls int
	err   error
}

func (v *testVerifier) Verify(context.Context, kernel.Transition, kernel.Authority) (kernel.Observation, error) {
	v.calls++
	if v.err != nil {
		return kernel.Observation{}, v.err
	}
	return kernel.NewObservation("svc", "ready", 1, time.Now().UTC())
}

func TestNewServerRequiresExecutorAndVerifier(t *testing.T) {
	runtime := kernel.NewRuntime("initial", kernel.AllowPolicy{})
	if _, err := NewServer(runtime, nil, &testVerifier{}); err == nil {
		t.Fatal("expected executor requirement")
	}
	if _, err := NewServer(runtime, &testExecutor{success: true}, nil); err == nil {
		t.Fatal("expected independent verifier requirement")
	}
}

func TestControlCommitsOnlyAfterIndependentVerification(t *testing.T) {
	executor := &testExecutor{success: true}
	verifier := &testVerifier{}
	runtime := kernel.NewRuntime("initial", kernel.AllowPolicy{})
	server, err := NewServer(runtime, executor, verifier)
	if err != nil {
		t.Fatal(err)
	}

	_, out, err := server.control(context.Background(), nil, ControlRequest{
		Subject:       "svc",
		ObservedState: "initial",
		DesiredState:  "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Verified || !out.Committed || out.Phase != kernel.PhaseCommitted {
		t.Fatalf("unexpected successful result: %+v", out)
	}
	if executor.calls != 1 || verifier.calls != 1 {
		t.Fatalf("expected one executor and verifier call, got %d and %d", executor.calls, verifier.calls)
	}
	if got := runtime.Root(); got != "ready" {
		t.Fatalf("root = %q, want ready", got)
	}
}

func TestControlRejectsNoopBeforeExecution(t *testing.T) {
	executor := &testExecutor{success: true}
	verifier := &testVerifier{}
	runtime := kernel.NewRuntime("ready", kernel.AllowPolicy{})
	server, err := NewServer(runtime, executor, verifier)
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = server.control(context.Background(), nil, ControlRequest{
		Subject:       "svc",
		ObservedState: "ready",
		DesiredState:  "ready",
	})
	if !errors.Is(err, kernel.ErrGovernanceDenied) {
		t.Fatalf("err = %v, want governance denial", err)
	}
	if executor.calls != 0 || verifier.calls != 0 {
		t.Fatalf("side-effect path ran for NOOP: executor=%d verifier=%d", executor.calls, verifier.calls)
	}
	if got := runtime.Root(); got != "ready" {
		t.Fatalf("root changed to %q", got)
	}
}

func TestControlFailsClosedWhenVerificationFails(t *testing.T) {
	executor := &testExecutor{success: true}
	verifier := &testVerifier{err: errors.New("independent evidence unavailable")}
	runtime := kernel.NewRuntime("initial", kernel.AllowPolicy{})
	server, err := NewServer(runtime, executor, verifier)
	if err != nil {
		t.Fatal(err)
	}

	_, out, err := server.control(context.Background(), nil, ControlRequest{
		Subject:       "svc",
		ObservedState: "initial",
		DesiredState:  "ready",
	})
	if err == nil {
		t.Fatal("expected verification failure")
	}
	if out.Verified || out.Committed {
		t.Fatalf("verification failure was reported as committed: %+v", out)
	}
	if runtime.Phase() != kernel.PhaseRecovery {
		t.Fatalf("phase = %s, want RECOVERY", runtime.Phase())
	}
	if got := runtime.Root(); got != "initial" {
		t.Fatalf("root changed to %q after failed verification", got)
	}
}
