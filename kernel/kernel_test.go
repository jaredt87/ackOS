package kernel

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeExecutor struct {
	result ExecutionResult
	mu     sync.Mutex
	calls  int
}

func (f *fakeExecutor) Execute(context.Context, Transition, Authority) ExecutionResult {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return f.result
}

type blockingExecutor struct {
	started chan struct{}
	release chan struct{}
	result  ExecutionResult
}

func (e *blockingExecutor) Execute(context.Context, Transition, Authority) ExecutionResult {
	close(e.started)
	<-e.release
	return e.result
}

type fakeVerifier struct {
	observation Observation
	err         error
	called      chan struct{}
}

func (f fakeVerifier) Verify(context.Context, Transition, Authority) (Observation, error) {
	if f.called != nil {
		close(f.called)
	}
	return f.observation, f.err
}

func observation(t *testing.T, subject, state string, version uint64, at time.Time) Observation {
	t.Helper()
	o, err := NewObservation(subject, state, version, at)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func authorize(t *testing.T, r *Runtime, o Observation, target string) (Transition, Authority) {
	t.Helper()
	if err := r.Observe(o); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Normalize(Proposal{Subject: o.Subject, TargetState: target}); err != nil {
		t.Fatal(err)
	}
	tr, err := r.Reconcile()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Govern(); err != nil {
		t.Fatal(err)
	}
	a, err := r.Reserve(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return tr, a
}

func TestLifecycleCommitRequiresIndependentVerification(t *testing.T) {
	now := time.Unix(100, 0)
	r := NewRuntime("A", nil)
	r.clock = func() time.Time { return now }
	o := observation(t, "resource", "A", 1, now)
	authorize(t, r, o, "B")
	if _, err := r.Start(context.Background(), &fakeExecutor{result: ExecutionResult{Success: true}}); err != nil {
		t.Fatal(err)
	}
	bad := observation(t, "resource", "C", 2, now.Add(time.Second))
	if err := r.Verify(context.Background(), fakeVerifier{observation: bad}); !errors.Is(err, ErrVerificationFailed) {
		t.Fatalf("expected verification failure, got %v", err)
	}
	if r.Root() != "A" {
		t.Fatalf("failed verification changed root: %s", r.Root())
	}
	if r.Phase() != PhaseRecovery {
		t.Fatalf("expected recovery, got %s", r.Phase())
	}
}

func TestAuthorityCannotBeReplayed(t *testing.T) {
	r := NewRuntime("A", nil)
	o := observation(t, "resource", "A", 1, time.Unix(100, 0))
	_, a := authorize(t, r, o, "B")
	if _, err := r.Start(context.Background(), &fakeExecutor{result: ExecutionResult{Success: false}}); err != nil {
		t.Fatal(err)
	}
	if a.Consumed {
		t.Fatalf("returned authority must be an immutable pre-consumption snapshot")
	}
	if r.phase != PhaseRecovery {
		t.Fatalf("expected recovery after failed execution")
	}
	if err := r.Observe(o); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Start(context.Background(), &fakeExecutor{}); !errors.Is(err, ErrInvalidLifecycle) {
		t.Fatalf("expected old authority to be unusable, got %v", err)
	}
}

func TestConcurrentCASOnlyOneSucceeds(t *testing.T) {
	s := NewStateStore("A")
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- s.CompareAndSwap("A", "B")
		}()
	}
	wg.Wait()
	close(results)
	var ok, conflicts int
	for err := range results {
		if err == nil {
			ok++
		} else if errors.Is(err, ErrCASConflict) {
			conflicts++
		}
	}
	if ok != 1 || conflicts != 1 {
		t.Fatalf("expected one success and one conflict, got success=%d conflict=%d", ok, conflicts)
	}
}

func TestConcurrentAuthorityStartAtMostOne(t *testing.T) {
	r := NewRuntime("A", nil)
	o := observation(t, "resource", "A", 1, time.Unix(100, 0))
	authorize(t, r, o, "B")
	e := &fakeExecutor{result: ExecutionResult{Success: true}}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.Start(context.Background(), e)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	var success, failure int
	for err := range results {
		if err == nil {
			success++
		} else {
			failure++
		}
	}
	if success != 1 || failure != 1 {
		t.Fatalf("expected one accepted execution, got success=%d failure=%d", success, failure)
	}
}

func TestObserveRejectedWhileExecutionInFlight(t *testing.T) {
	now := time.Unix(100, 0)
	r := NewRuntime("A", nil)
	r.clock = func() time.Time { return now }
	o := observation(t, "resource", "A", 1, now)
	authorize(t, r, o, "B")
	exec := &blockingExecutor{started: make(chan struct{}), release: make(chan struct{}), result: ExecutionResult{Success: true}}
	startDone := make(chan error, 1)
	go func() {
		_, err := r.Start(context.Background(), exec)
		startDone <- err
	}()
	<-exec.started

	o2 := observation(t, "resource", "A", 2, now.Add(time.Second))
	if err := r.Observe(o2); !errors.Is(err, ErrInvalidLifecycle) {
		t.Fatalf("expected in-flight observation to be rejected, got %v", err)
	}
	if r.Phase() != PhaseStarted {
		t.Fatalf("expected started phase to remain intact, got %s", r.Phase())
	}

	close(exec.release)
	if err := <-startDone; err != nil {
		t.Fatal(err)
	}
}

func TestVerifyWaitsForExecutionCompletion(t *testing.T) {
	now := time.Unix(100, 0)
	r := NewRuntime("A", nil)
	r.clock = func() time.Time { return now }
	o := observation(t, "resource", "A", 1, now)
	authorize(t, r, o, "B")
	exec := &blockingExecutor{started: make(chan struct{}), release: make(chan struct{}), result: ExecutionResult{Success: true}}
	startDone := make(chan error, 1)
	go func() {
		_, err := r.Start(context.Background(), exec)
		startDone <- err
	}()
	<-exec.started

	verified := make(chan error, 1)
	called := make(chan struct{})
	post := observation(t, "resource", "B", 2, now.Add(time.Second))
	go func() {
		verified <- r.Verify(context.Background(), fakeVerifier{observation: post, called: called})
	}()
	select {
	case <-called:
		t.Fatal("verifier ran before execution completed")
	case <-verified:
		t.Fatal("verification returned before execution completed")
	case <-time.After(50 * time.Millisecond):
	}

	close(exec.release)
	if err := <-startDone; err != nil {
		t.Fatal(err)
	}
	if err := <-verified; err != nil {
		t.Fatal(err)
	}
	if r.Phase() != PhaseVerified {
		t.Fatalf("expected verified phase, got %s", r.Phase())
	}
}

func TestConcurrentVerifyAllowsOnlyOneVerifier(t *testing.T) {
	now := time.Unix(100, 0)
	r := NewRuntime("A", nil)
	r.clock = func() time.Time { return now }
	o := observation(t, "resource", "A", 1, now)
	authorize(t, r, o, "B")
	if _, err := r.Start(context.Background(), &fakeExecutor{result: ExecutionResult{Success: true}}); err != nil {
		t.Fatal(err)
	}
	post := observation(t, "resource", "B", 2, now.Add(time.Second))
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	first := blockingVerifier{entered: firstEntered, release: releaseFirst, observation: post}
	firstDone := make(chan error, 1)
	go func() { firstDone <- r.Verify(context.Background(), &first) }()
	<-firstEntered

	if err := r.Verify(context.Background(), fakeVerifier{observation: post}); !errors.Is(err, ErrInvalidLifecycle) {
		t.Fatalf("expected second verifier to be rejected, got %v", err)
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if r.Phase() != PhaseVerified {
		t.Fatalf("expected verified phase, got %s", r.Phase())
	}
}

type blockingVerifier struct {
	entered     chan struct{}
	release     chan struct{}
	observation Observation
}

func (v *blockingVerifier) Verify(context.Context, Transition, Authority) (Observation, error) {
	close(v.entered)
	<-v.release
	return v.observation, nil
}

func TestFailedExecutionCannotBeVerified(t *testing.T) {
	now := time.Unix(100, 0)
	r := NewRuntime("A", nil)
	r.clock = func() time.Time { return now }
	o := observation(t, "resource", "A", 1, now)
	authorize(t, r, o, "B")
	if _, err := r.Start(context.Background(), &fakeExecutor{result: ExecutionResult{Success: false}}); err != nil {
		t.Fatal(err)
	}
	if err := r.Verify(context.Background(), fakeVerifier{observation: observation(t, "resource", "B", 2, now.Add(time.Second))}); !errors.Is(err, ErrInvalidLifecycle) {
		t.Fatalf("expected verification to be rejected after failed execution, got %v", err)
	}
}

func TestVerificationRejectsPreExecutionEvidence(t *testing.T) {
	now := time.Unix(100, 0)
	r := NewRuntime("A", nil)
	r.clock = func() time.Time { return now }
	o := observation(t, "resource", "A", 1, now)
	authorize(t, r, o, "B")
	if _, err := r.Start(context.Background(), &fakeExecutor{result: ExecutionResult{Success: true}}); err != nil {
		t.Fatal(err)
	}
	cached := observation(t, "resource", "B", 1, now.Add(-time.Second))
	if err := r.Verify(context.Background(), fakeVerifier{observation: cached}); !errors.Is(err, ErrVerificationFailed) {
		t.Fatalf("expected pre-execution evidence to be rejected, got %v", err)
	}
}

func TestVerificationRejectsHigherVersionPreExecutionEvidence(t *testing.T) {
	now := time.Unix(100, 0)
	r := NewRuntime("A", nil)
	r.clock = func() time.Time { return now }
	o := observation(t, "resource", "A", 1, now)
	authorize(t, r, o, "B")
	if _, err := r.Start(context.Background(), &fakeExecutor{result: ExecutionResult{Success: true}}); err != nil {
		t.Fatal(err)
	}
	cached := observation(t, "resource", "B", 2, now.Add(-time.Second))
	if err := r.Verify(context.Background(), fakeVerifier{observation: cached}); !errors.Is(err, ErrVerificationFailed) {
		t.Fatalf("expected higher-version pre-execution evidence to be rejected, got %v", err)
	}
}

func TestObservationTimestampIsBoundToFingerprint(t *testing.T) {
	o := observation(t, "resource", "A", 1, time.Unix(100, 0))
	o.ObservedAt = o.ObservedAt.Add(time.Hour)
	if !errors.Is(o.Validate(), ErrStaleEvidence) {
		t.Fatalf("expected timestamp tampering to invalidate evidence, got %v", o.Validate())
	}
}

func TestRecoverRequiresFreshEvidence(t *testing.T) {
	now := time.Unix(100, 0)
	r := NewRuntime("A", nil)
	r.clock = func() time.Time { return now }
	o := observation(t, "resource", "A", 1, now)
	authorize(t, r, o, "B")
	if _, err := r.Start(context.Background(), &fakeExecutor{result: ExecutionResult{Success: false}}); err != nil {
		t.Fatal(err)
	}
	if err := r.Recover(o); !errors.Is(err, ErrStaleEvidence) {
		t.Fatalf("expected old recovery evidence to be rejected, got %v", err)
	}
	freshVersionButOldTime := observation(t, "resource", "A", 2, now.Add(-time.Second))
	if err := r.Recover(freshVersionButOldTime); !errors.Is(err, ErrStaleEvidence) {
		t.Fatalf("expected pre-attempt higher-version evidence to be rejected, got %v", err)
	}
	fresh := observation(t, "resource", "A", 2, now.Add(time.Second))
	if err := r.Recover(fresh); err != nil {
		t.Fatalf("expected post-attempt recovery evidence to be accepted, got %v", err)
	}
	if r.Phase() != PhaseObserved {
		t.Fatalf("expected observed phase after recovery, got %s", r.Phase())
	}
}

func TestExpiredAuthority(t *testing.T) {
	now := time.Unix(100, 0)
	o := observation(t, "resource", "A", 1, now)
	r := NewRuntime("A", nil)
	r.clock = func() time.Time { return now }
	authorize(t, r, o, "B")
	r = NewRuntime("A", nil)
	r.clock = func() time.Time { return now }
	if err := r.Observe(o); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Normalize(Proposal{Subject: "resource", TargetState: "B"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Govern(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reserve(time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	r.clock = func() time.Time { return now.Add(time.Second) }
	if _, err := r.Start(context.Background(), &fakeExecutor{}); !errors.Is(err, ErrAuthorityExpired) {
		t.Fatalf("expected expiration, got %v", err)
	}
}

func TestStaleObservationInvalidatesAuthorization(t *testing.T) {
	r := NewRuntime("A", nil)
	o1 := observation(t, "resource", "A", 1, time.Unix(100, 0))
	authorize(t, r, o1, "B")
	o2 := observation(t, "resource", "A", 2, time.Unix(101, 0))
	if err := r.Observe(o2); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Start(context.Background(), &fakeExecutor{}); !errors.Is(err, ErrInvalidLifecycle) {
		t.Fatalf("expected stale authorization to be cleared, got %v", err)
	}
}

func TestDeterministicNormalizationAndReconciliation(t *testing.T) {
	o := observation(t, "resource", "A", 1, time.Unix(100, 0))
	p := Proposal{Subject: "resource", TargetState: "B"}
	n1, err := Normalize(p)
	if err != nil {
		t.Fatal(err)
	}
	n2, err := Normalize(p)
	if err != nil {
		t.Fatal(err)
	}
	if n1 != n2 {
		t.Fatal("normalization is not deterministic")
	}
	t1, err := Reconcile(o, n1)
	if err != nil {
		t.Fatal(err)
	}
	t2, err := Reconcile(o, n2)
	if err != nil {
		t.Fatal(err)
	}
	if t1 != t2 {
		t.Fatal("reconciliation is not deterministic")
	}
}

func TestCASConflictPreventsCommit(t *testing.T) {
	now := time.Unix(100, 0)
	r := NewRuntime("A", nil)
	r.clock = func() time.Time { return now }
	o := observation(t, "resource", "A", 1, now)
	authorize(t, r, o, "B")
	if _, err := r.Start(context.Background(), &fakeExecutor{result: ExecutionResult{Success: true}}); err != nil {
		t.Fatal(err)
	}
	post := observation(t, "resource", "B", 2, now.Add(time.Second))
	if err := r.Verify(context.Background(), fakeVerifier{observation: post}); err != nil {
		t.Fatal(err)
	}
	if err := r.store.CompareAndSwap("A", "C"); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(r.Commit(), ErrCASConflict) {
		t.Fatalf("expected CAS conflict")
	}
	if r.Root() != "C" {
		t.Fatalf("conflicting commit overwrote root: %s", r.Root())
	}
}
