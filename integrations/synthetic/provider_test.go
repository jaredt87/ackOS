package synthetic

import (
	"context"
	"testing"
	"time"

	"github.com/jaredt87/ackOS/kernel"
)

func authorize(t *testing.T, runtime *kernel.Runtime, subject, before, after string) (kernel.Transition, kernel.Authority) {
	t.Helper()
	observation, err := kernel.NewObservation(subject, before, 1, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Observe(observation); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Normalize(kernel.Proposal{Subject: subject, TargetState: after}); err != nil {
		t.Fatal(err)
	}
	transition, err := runtime.Reconcile()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Govern(); err != nil {
		t.Fatal(err)
	}
	authority, err := runtime.Reserve(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return transition, authority
}

func TestProviderHappyPath(t *testing.T) {
	resource := NewResource("resource-a", "initial")
	runtime := kernel.NewRuntime("initial", kernel.AllowPolicy{})
	executor := Executor{Resource: resource}
	verifier := Verifier{Resource: resource}

	transition, _ := authorize(t, runtime, "resource-a", "initial", "running")
	result, err := runtime.Start(context.Background(), executor)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("execution failed: %s", result.Message)
	}
	if err := runtime.Verify(context.Background(), verifier); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := runtime.Root(); got != "running" {
		t.Fatalf("root = %q, want running", got)
	}
	if subject, state, _ := resource.Observe(); subject != transition.Subject || state != transition.After {
		t.Fatalf("resource = (%q, %q), want (%q, %q)", subject, state, transition.Subject, transition.After)
	}
}

func TestExecutorRejectsResourceSubstitution(t *testing.T) {
	resource := NewResource("resource-a", "initial")
	runtime := kernel.NewRuntime("initial", kernel.AllowPolicy{})

	authorize(t, runtime, "resource-a", "initial", "running")
	resource.Set("resource-b", "initial")

	result, err := runtime.Start(context.Background(), Executor{Resource: resource})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success {
		t.Fatal("execution unexpectedly succeeded for substituted resource")
	}
	if runtime.Phase() != kernel.PhaseRecovery {
		t.Fatalf("phase = %s, want RECOVERY", runtime.Phase())
	}
	if got := runtime.Root(); got != "initial" {
		t.Fatalf("root = %q, want initial", got)
	}
}

func TestExecutorRejectsTOCTOUStateChange(t *testing.T) {
	resource := NewResource("resource-a", "initial")
	runtime := kernel.NewRuntime("initial", kernel.AllowPolicy{})

	authorize(t, runtime, "resource-a", "initial", "running")
	resource.Set("resource-a", "changed-outside-ackos")

	result, err := runtime.Start(context.Background(), Executor{Resource: resource})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success {
		t.Fatal("execution unexpectedly succeeded after TOCTOU mutation")
	}
	if runtime.Phase() != kernel.PhaseRecovery {
		t.Fatalf("phase = %s, want RECOVERY", runtime.Phase())
	}
	if _, state, _ := resource.Observe(); state != "changed-outside-ackos" {
		t.Fatalf("resource state was unexpectedly overwritten")
	}
}

type noOpExecutor struct{}

func (noOpExecutor) Execute(context.Context, kernel.Transition, kernel.Authority) kernel.ExecutionResult {
	return kernel.ExecutionResult{Success: true, Message: "claimed success without changing the resource"}
}

func TestIndependentVerifierRejectsFalseExecutionSuccess(t *testing.T) {
	resource := NewResource("resource-a", "initial")
	runtime := kernel.NewRuntime("initial", kernel.AllowPolicy{})
	verifier := Verifier{Resource: resource}

	authorize(t, runtime, "resource-a", "initial", "running")
	result, err := runtime.Start(context.Background(), noOpExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("unexpected executor failure: %s", result.Message)
	}
	if err := runtime.Verify(context.Background(), verifier); err == nil {
		t.Fatal("verification unexpectedly succeeded without an external state change")
	}
	if runtime.Phase() != kernel.PhaseRecovery {
		t.Fatalf("phase = %s, want RECOVERY", runtime.Phase())
	}
	if got := runtime.Root(); got != "initial" {
		t.Fatalf("root = %q, want initial", got)
	}
}

func TestVerifierRejectsOutOfBandMatchingState(t *testing.T) {
	resource := NewResource("resource-a", "initial")
	runtime := kernel.NewRuntime("initial", kernel.AllowPolicy{})
	verifier := Verifier{Resource: resource}

	authorize(t, runtime, "resource-a", "initial", "running")
	result, err := runtime.Start(context.Background(), noOpExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("unexpected executor failure: %s", result.Message)
	}

	// An out-of-band actor reaches the desired state without performing the
	// authorized execution. Matching state alone must not be enough to commit.
	resource.Set("resource-a", "running")
	if err := runtime.Verify(context.Background(), verifier); err == nil {
		t.Fatal("verification unexpectedly accepted matching state without execution binding")
	}
	if runtime.Phase() != kernel.PhaseRecovery {
		t.Fatalf("phase = %s, want RECOVERY", runtime.Phase())
	}
	if got := runtime.Root(); got != "initial" {
		t.Fatalf("root = %q, want initial", got)
	}
}

func TestVerifierUsesFreshExternalObservation(t *testing.T) {
	resource := NewResource("resource-a", "initial")
	runtime := kernel.NewRuntime("initial", kernel.AllowPolicy{})
	verifier := Verifier{Resource: resource}

	authorize(t, runtime, "resource-a", "initial", "running")
	if _, err := runtime.Start(context.Background(), Executor{Resource: resource}); err != nil {
		t.Fatal(err)
	}

	// Mutate the external resource after execution but before verification.
	// The verifier must see this fresh state and refuse commitment.
	resource.Set("resource-a", "externally-changed")
	if err := runtime.Verify(context.Background(), verifier); err == nil {
		t.Fatal("verification unexpectedly ignored a post-execution external mutation")
	}
	if runtime.Phase() != kernel.PhaseRecovery {
		t.Fatalf("phase = %s, want RECOVERY", runtime.Phase())
	}
	if got := runtime.Root(); got != "initial" {
		t.Fatalf("root = %q, want initial", got)
	}
}
