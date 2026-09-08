package kernel

import (
	"context"
	"errors"
	"testing"
	"time"
)

type mismatchedPolicy struct{}

func (mismatchedPolicy) Govern(Transition) GovernanceDecision {
	return GovernanceDecision{Allowed: true}
}

func TestGovernRejectsMismatchedDecision(t *testing.T) {
	r := NewRuntime("A", mismatchedPolicy{})
	o := observation(t, "resource", "A", 1, time.Unix(100, 0))
	if err := r.Observe(o); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Normalize(Proposal{Subject: "resource", TargetState: "B"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Govern(); !errors.Is(err, ErrGovernanceDenied) {
		t.Fatalf("expected governance denial, got %v", err)
	}
}

func TestObserveRejectedDuringRecovery(t *testing.T) {
	now := time.Unix(100, 0)
	r := NewRuntime("A", nil)
	r.clock = func() time.Time { return now }
	o := observation(t, "resource", "A", 1, now)
	authorize(t, r, o, "B")
	if _, err := r.Start(context.Background(), &fakeExecutor{result: ExecutionResult{Success: false}}); err != nil {
		t.Fatal(err)
	}
	fresh := observation(t, "resource", "A", 2, now.Add(time.Second))
	if err := r.Observe(fresh); !errors.Is(err, ErrInvalidLifecycle) {
		t.Fatalf("expected recovery observation to be rejected, got %v", err)
	}
	if r.Phase() != PhaseRecovery {
		t.Fatalf("expected recovery phase, got %s", r.Phase())
	}
}

func TestRecoverRejectsUnrelatedSubject(t *testing.T) {
	now := time.Unix(100, 0)
	r := NewRuntime("A", nil)
	r.clock = func() time.Time { return now }
	o := observation(t, "resource-a", "A", 1, now)
	authorize(t, r, o, "B")
	if _, err := r.Start(context.Background(), &fakeExecutor{result: ExecutionResult{Success: false}}); err != nil {
		t.Fatal(err)
	}
	unrelated := observation(t, "resource-b", "A", 2, now.Add(time.Second))
	if err := r.Recover(unrelated); !errors.Is(err, ErrInvalidObservation) {
		t.Fatalf("expected unrelated recovery evidence to be rejected, got %v", err)
	}
}

func TestVerifyCancellationReleasesReservation(t *testing.T) {
	now := time.Unix(100, 0)
	r := NewRuntime("A", nil)
	r.clock = func() time.Time { return now }
	o := observation(t, "resource", "A", 1, now)
	authorize(t, r, o, "B")
	exec := &blockingExecutor{
		started: make(chan struct{}),
		release: make(chan struct{}),
		result:  ExecutionResult{Success: true},
	}
	startDone := make(chan error, 1)
	go func() {
		_, err := r.Start(context.Background(), exec)
		startDone <- err
	}()
	<-exec.started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.Verify(ctx, fakeVerifier{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if r.Phase() != PhaseStarted {
		t.Fatalf("expected started phase after cancellation, got %s", r.Phase())
	}
	close(exec.release)
	if err := <-startDone; err != nil {
		t.Fatal(err)
	}
}

func TestStaleVerificationCannotClearLaterReservation(t *testing.T) {
	r := NewRuntime("A", nil)
	o := observation(t, "resource", "A", 1, time.Unix(100, 0))
	authorize(t, r, o, "B")

	oldDone := make(chan struct{})
	newDone := make(chan struct{})
	r.mu.Lock()
	r.phase = PhaseStarted
	r.executionDone = oldDone
	r.verificationActive = false
	r.mu.Unlock()

	verifyDone := make(chan error, 1)
	go func() {
		verifyDone <- r.Verify(context.Background(), fakeVerifier{})
	}()

	// Wait until Verify has claimed the old lifecycle's reservation.
	for {
		r.mu.Lock()
		active := r.verificationActive
		r.mu.Unlock()
		if active {
			break
		}
		time.Sleep(time.Millisecond)
	}

	// Simulate recovery and a new execution lifecycle before the stale verifier wakes.
	r.mu.Lock()
	r.executionDone = newDone
	r.verificationActive = true
	r.mu.Unlock()
	close(oldDone)

	if err := <-verifyDone; !errors.Is(err, ErrInvalidLifecycle) {
		t.Fatalf("expected stale verifier to be rejected, got %v", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.verificationActive {
		t.Fatal("stale verifier cleared the later reservation")
	}
	r.verificationActive = false
}
