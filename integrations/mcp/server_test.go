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
	calls       int
	err         error
	block       <-chan struct{}
	observeErr  error
	observeDone <-chan struct{}
}

func (v *testVerifier) Verify(ctx context.Context, _ kernel.Transition, _ kernel.Authority) (kernel.Observation, error) {
	v.calls++
	if v.block != nil {
		select {
		case <-v.block:
		case <-ctx.Done():
			return kernel.Observation{}, ctx.Err()
		}
	}
	if v.err != nil {
		return kernel.Observation{}, v.err
	}
	return kernel.NewObservation("svc", "ready", 1, time.Now().UTC())
}

func (v *testVerifier) Observe(ctx context.Context, subject string) (kernel.Observation, error) {
	if v.observeDone != nil {
		select {
		case <-v.observeDone:
		case <-ctx.Done():
			return kernel.Observation{}, ctx.Err()
		}
	}
	if v.observeErr != nil {
		return kernel.Observation{}, v.observeErr
	}
	return kernel.NewObservation(subject, "initial", 1, time.Now().UTC())
}

func TestNewServerRequiresExecutorVerifierAndRecoveryObserver(t *testing.T) {
	runtime := kernel.NewRuntime("initial", kernel.AllowPolicy{})
	if _, err := NewServer(runtime, nil, &testVerifier{}, &testVerifier{}); err == nil {
		t.Fatal("expected executor requirement")
	}
	if _, err := NewServer(runtime, &testExecutor{success: true}, nil, &testVerifier{}); err == nil {
		t.Fatal("expected independent verifier requirement")
	}
	if _, err := NewServer(runtime, &testExecutor{success: true}, &testVerifier{}, nil); err == nil {
		t.Fatal("expected independent recovery observer requirement")
	}
}

func newTestServer(t *testing.T, runtime *kernel.Runtime, executor *testExecutor, verifier *testVerifier) *Server {
	t.Helper()
	server, err := NewServer(runtime, executor, verifier, verifier)
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func TestControlRejectsCanceledRequestBeforeLifecycleAdmission(t *testing.T) {
	executor := &testExecutor{success: true}
	verifier := &testVerifier{}
	runtime := kernel.NewRuntime("initial", kernel.AllowPolicy{})
	server := newTestServer(t, runtime, executor, verifier)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := server.control(ctx, nil, ControlRequest{Subject: "svc", ObservedState: "initial", DesiredState: "ready"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if executor.calls != 0 || verifier.calls != 0 || runtime.Phase() != kernel.PhaseIdle {
		t.Fatalf("canceled request entered lifecycle: executor=%d verifier=%d phase=%s", executor.calls, verifier.calls, runtime.Phase())
	}
}

func TestControlRejectsCanceledRequestAfterSerializationWait(t *testing.T) {
	executor := &testExecutor{success: true}
	verifier := &testVerifier{}
	runtime := kernel.NewRuntime("initial", kernel.AllowPolicy{})
	server := newTestServer(t, runtime, executor, verifier)

	server.controlMu.Lock()
	defer server.controlMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() {
		_, _, err := server.control(ctx, nil, ControlRequest{Subject: "svc", ObservedState: "initial", DesiredState: "ready"})
		done <- err
	}()

	time.Sleep(10 * time.Millisecond)
	server.controlMu.Unlock()
	defer func() { server.controlMu.Lock() }()

	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if executor.calls != 0 || verifier.calls != 0 || runtime.Phase() != kernel.PhaseIdle {
		t.Fatalf("canceled request entered lifecycle: executor=%d verifier=%d phase=%s", executor.calls, verifier.calls, runtime.Phase())
	}
}

func TestControlRejectsAuthorityTTLOverflow(t *testing.T) {
	executor := &testExecutor{success: true}
	verifier := &testVerifier{}
	runtime := kernel.NewRuntime("initial", kernel.AllowPolicy{})
	server := newTestServer(t, runtime, executor, verifier)

	const maxInt64 = int64(^uint64(0) >> 1)
	_, _, err := server.control(context.Background(), nil, ControlRequest{
		Subject:        "svc",
		ObservedState:  "initial",
		DesiredState:   "ready",
		AuthorityTTLMS: maxInt64,
	})
	if err == nil {
		t.Fatal("expected TTL overflow rejection")
	}
	if executor.calls != 0 || verifier.calls != 0 || runtime.Phase() != kernel.PhaseIdle {
		t.Fatalf("overflowing TTL entered lifecycle: executor=%d verifier=%d phase=%s", executor.calls, verifier.calls, runtime.Phase())
	}
}

func TestControlCommitsOnlyAfterIndependentVerification(t *testing.T) {
	executor := &testExecutor{success: true}
	verifier := &testVerifier{}
	runtime := kernel.NewRuntime("initial", kernel.AllowPolicy{})
	server := newTestServer(t, runtime, executor, verifier)

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
	if !out.Authority.Consumed {
		t.Fatalf("authority = %+v, want consumed", out.Authority)
	}
	if executor.calls != 1 || verifier.calls != 1 {
		t.Fatalf("expected one executor and verifier call, got %d and %d", executor.calls, verifier.calls)
	}
	if got := runtime.Root(); got != "ready" {
		t.Fatalf("root = %q, want ready", got)
	}
}

func TestControlBoundsIndependentVerification(t *testing.T) {
	executor := &testExecutor{success: true}
	verifier := &testVerifier{block: make(chan struct{})}
	runtime := kernel.NewRuntime("initial", kernel.AllowPolicy{})
	server := newTestServer(t, runtime, executor, verifier)
	server.verifyTimeout = 10 * time.Millisecond

	_, out, err := server.control(context.Background(), nil, ControlRequest{
		Subject:       "svc",
		ObservedState: "initial",
		DesiredState:  "ready",
	})
	if err == nil {
		t.Fatal("expected bounded verification failure")
	}
	if out.Verified || out.Committed || out.Phase != kernel.PhaseRecovery {
		t.Fatalf("unexpected timeout result: %+v", out)
	}
	if !out.Authority.Consumed {
		t.Fatalf("authority = %+v, want consumed after Start", out.Authority)
	}
	if runtime.Root() != "initial" {
		t.Fatalf("root changed after verification timeout: %q", runtime.Root())
	}
}

func TestControlRejectsNoopBeforeExecution(t *testing.T) {
	executor := &testExecutor{success: true}
	verifier := &testVerifier{}
	runtime := kernel.NewRuntime("ready", kernel.AllowPolicy{})
	server := newTestServer(t, runtime, executor, verifier)

	_, _, err := server.control(context.Background(), nil, ControlRequest{
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
	server := newTestServer(t, runtime, executor, verifier)

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

func TestControlRecoversUsingIndependentProviderEvidence(t *testing.T) {
	executor := &testExecutor{success: true}
	verifier := &testVerifier{err: errors.New("temporary evidence failure")}
	runtime := kernel.NewRuntime("initial", kernel.AllowPolicy{})
	server := newTestServer(t, runtime, executor, verifier)

	_, _, err := server.control(context.Background(), nil, ControlRequest{
		Subject:       "svc",
		ObservedState: "initial",
		DesiredState:  "ready",
	})
	if err == nil || runtime.Phase() != kernel.PhaseRecovery {
		t.Fatalf("expected verification failure and recovery, err=%v phase=%s", err, runtime.Phase())
	}

	verifier.err = nil
	_, out, err := server.control(context.Background(), nil, ControlRequest{
		Subject:       "svc",
		ObservedState: "bogus-stale-caller-state",
		DesiredState:  "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Verified || !out.Committed || runtime.Phase() != kernel.PhaseCommitted {
		t.Fatalf("unexpected recovered result: %+v", out)
	}
	if executor.calls != 2 || verifier.calls != 2 {
		t.Fatalf("expected fresh execution and verification after recovery, got %d and %d", executor.calls, verifier.calls)
	}
	if out.Observation.State != "initial" {
		t.Fatalf("recovery trusted caller state instead of provider evidence: %+v", out.Observation)
	}
}

func TestControlRecoversAfterExecutionFailure(t *testing.T) {
	executor := &testExecutor{}
	verifier := &testVerifier{}
	runtime := kernel.NewRuntime("initial", kernel.AllowPolicy{})
	server := newTestServer(t, runtime, executor, verifier)

	_, _, err := server.control(context.Background(), nil, ControlRequest{
		Subject:       "svc",
		ObservedState: "initial",
		DesiredState:  "ready",
	})
	if err == nil || runtime.Phase() != kernel.PhaseRecovery {
		t.Fatalf("expected execution failure and recovery, err=%v phase=%s", err, runtime.Phase())
	}

	executor.success = true
	_, out, err := server.control(context.Background(), nil, ControlRequest{
		Subject:       "svc",
		ObservedState: "initial",
		DesiredState:  "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Verified || !out.Committed || runtime.Phase() != kernel.PhaseCommitted {
		t.Fatalf("unexpected recovered result: %+v", out)
	}
}

func TestControlBoundsRecoveryObservation(t *testing.T) {
	executor := &testExecutor{}
	verifier := &testVerifier{observeDone: make(chan struct{})}
	runtime := kernel.NewRuntime("initial", kernel.AllowPolicy{})
	server := newTestServer(t, runtime, executor, verifier)
	server.verifyTimeout = 10 * time.Millisecond

	_, _, err := server.control(context.Background(), nil, ControlRequest{
		Subject:       "svc",
		ObservedState: "initial",
		DesiredState:  "ready",
	})
	if err == nil || runtime.Phase() != kernel.PhaseRecovery {
		t.Fatalf("expected execution failure and recovery, err=%v phase=%s", err, runtime.Phase())
	}

	_, out, err := server.control(context.Background(), nil, ControlRequest{
		Subject:       "svc",
		ObservedState: "initial",
		DesiredState:  "ready",
	})
	if err == nil {
		t.Fatal("expected recovery observation timeout")
	}
	if out.Phase != kernel.PhaseRecovery || runtime.Phase() != kernel.PhaseRecovery {
		t.Fatalf("timeout should leave runtime recoverable: out=%+v phase=%s", out, runtime.Phase())
	}
}

func TestControlDoesNotOverlapTimedOutProviderCall(t *testing.T) {
	executor := &testExecutor{success: true}
	verifier := &testVerifier{block: make(chan struct{})}
	runtime := kernel.NewRuntime("initial", kernel.AllowPolicy{})
	server := newTestServer(t, runtime, executor, verifier)
	server.verifyTimeout = 10 * time.Millisecond

	_, _, err := server.control(context.Background(), nil, ControlRequest{
		Subject:       "svc",
		ObservedState: "initial",
		DesiredState:  "ready",
	})
	if err == nil || runtime.Phase() != kernel.PhaseRecovery {
		t.Fatalf("expected verification timeout and recovery, err=%v phase=%s", err, runtime.Phase())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, _, err = server.control(ctx, nil, ControlRequest{
		Subject:       "svc",
		ObservedState: "initial",
		DesiredState:  "ready",
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context deadline while prior verifier remains in flight", err)
	}
	if verifier.calls != 1 {
		t.Fatalf("overlapping provider call started: verifier calls=%d, want 1", verifier.calls)
	}

	close(verifier.block)
}
