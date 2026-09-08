// Package kernel implements the minimal ackOS autonomous-control boundary.
package kernel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	ErrInvalidObservation = errors.New("invalid observation")
	ErrStaleEvidence      = errors.New("stale evidence")
	ErrInvalidProposal    = errors.New("invalid proposal")
	ErrInvalidTransition  = errors.New("invalid transition")
	ErrGovernanceDenied   = errors.New("governance denied")
	ErrAuthorityMissing   = errors.New("authority missing")
	ErrAuthorityExpired   = errors.New("authority expired")
	ErrAuthorityConsumed  = errors.New("authority already consumed")
	ErrAuthorityMismatch  = errors.New("authority mismatch")
	ErrVerificationFailed = errors.New("verification failed")
	ErrCASConflict        = errors.New("compare-and-swap conflict")
	ErrInvalidLifecycle   = errors.New("invalid lifecycle transition")
)

type Decision string

const (
	DecisionNoop    Decision = "NOOP"
	DecisionExecute Decision = "EXECUTE"
	DecisionStale   Decision = "STALE"
)

type Phase string

const (
	PhaseIdle       Phase = "IDLE"
	PhaseObserved   Phase = "OBSERVED"
	PhaseNormalized Phase = "NORMALIZED"
	PhaseReconciled Phase = "RECONCILED"
	PhaseAuthorized Phase = "AUTHORIZED"
	PhaseReserved   Phase = "RESERVED"
	PhaseStarted    Phase = "STARTED"
	PhaseVerified   Phase = "VERIFIED"
	PhaseCommitted  Phase = "COMMITTED"
	PhaseRecovery   Phase = "RECOVERY"
	PhaseFailed     Phase = "FAILED"
)

type Observation struct {
	Subject     string
	State       string
	Version     uint64
	ObservedAt  time.Time
	Fingerprint string
}

func NewObservation(subject, state string, version uint64, observedAt time.Time) (Observation, error) {
	if subject == "" {
		return Observation{}, fmt.Errorf("%w: subject is required", ErrInvalidObservation)
	}
	if state == "" {
		return Observation{}, fmt.Errorf("%w: state is required", ErrInvalidObservation)
	}
	if observedAt.IsZero() {
		return Observation{}, fmt.Errorf("%w: observed time is required", ErrInvalidObservation)
	}
	o := Observation{Subject: subject, State: state, Version: version, ObservedAt: observedAt.UTC()}
	fp, err := observationFingerprint(o)
	if err != nil {
		return Observation{}, fmt.Errorf("%w: observation fingerprint serialization failed: %v", ErrInvalidObservation, err)
	}
	o.Fingerprint = fp
	return o, nil
}

func (o Observation) Validate() error {
	if o.Subject == "" || o.State == "" || o.ObservedAt.IsZero() || o.Fingerprint == "" {
		return ErrInvalidObservation
	}
	fp, err := observationFingerprint(o)
	if err != nil {
		return fmt.Errorf("%w: observation fingerprint serialization failed: %v", ErrInvalidObservation, err)
	}
	if fp != o.Fingerprint {
		return ErrStaleEvidence
	}
	return nil
}

func observationFingerprint(o Observation) (string, error) {
	b, err := json.Marshal(struct {
		Subject    string
		State      string
		Version    uint64
		ObservedAt time.Time
	}{o.Subject, o.State, o.Version, o.ObservedAt.UTC()})
	if err != nil {
		return "", err
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:]), nil
}

type Proposal struct {
	Subject     string
	TargetState string
}

type NormalizedProposal struct {
	Subject     string
	TargetState string
	Fingerprint string
}

func Normalize(p Proposal) (NormalizedProposal, error) {
	if p.Subject == "" || p.TargetState == "" {
		return NormalizedProposal{}, ErrInvalidProposal
	}
	n := NormalizedProposal{Subject: p.Subject, TargetState: p.TargetState}
	n.Fingerprint = fingerprint(struct {
		Subject     string
		TargetState string
	}{n.Subject, n.TargetState})
	return n, nil
}

func (n NormalizedProposal) Validate() error {
	if n.Subject == "" || n.TargetState == "" || n.Fingerprint == "" {
		return ErrInvalidProposal
	}
	if fingerprint(struct {
		Subject     string
		TargetState string
	}{n.Subject, n.TargetState}) != n.Fingerprint {
		return ErrStaleEvidence
	}
	return nil
}

type Transition struct {
	ID                     string
	Subject                string
	Decision               Decision
	Before                 string
	After                  string
	ObservationFingerprint string
	ProposalFingerprint    string
	Fingerprint            string
}

func Reconcile(o Observation, p NormalizedProposal) (Transition, error) {
	if err := o.Validate(); err != nil {
		return Transition{}, err
	}
	if err := p.Validate(); err != nil {
		return Transition{}, err
	}
	if o.Subject != p.Subject {
		return Transition{}, ErrInvalidTransition
	}
	t := Transition{Subject: o.Subject, Before: o.State, After: p.TargetState, ObservationFingerprint: o.Fingerprint, ProposalFingerprint: p.Fingerprint}
	if p.TargetState == o.State {
		t.Decision = DecisionNoop
	} else {
		t.Decision = DecisionExecute
	}
	t.Fingerprint = fingerprint(struct {
		Subject, Before, After, ObservationFingerprint, ProposalFingerprint string
		Decision                                                            Decision
	}{t.Subject, t.Before, t.After, t.ObservationFingerprint, t.ProposalFingerprint, t.Decision})
	t.ID = t.Fingerprint
	return t, nil
}

type GovernanceDecision struct {
	Allowed                bool
	Reason                 string
	TransitionFingerprint  string
	ObservationFingerprint string
	ProposalFingerprint    string
}

type Policy interface {
	Govern(Transition) GovernanceDecision
}

type AllowPolicy struct{}

func (AllowPolicy) Govern(t Transition) GovernanceDecision {
	allowed := t.Decision == DecisionExecute
	reason := "transition is executable"
	if t.Decision == DecisionNoop {
		reason = "desired state already observed"
	}
	if !allowed && t.Decision != DecisionNoop {
		reason = "transition is stale or invalid"
	}
	return GovernanceDecision{Allowed: allowed, Reason: reason, TransitionFingerprint: t.Fingerprint, ObservationFingerprint: t.ObservationFingerprint, ProposalFingerprint: t.ProposalFingerprint}
}

type Authority struct {
	ExecutionID            string
	TransitionFingerprint  string
	ObservationFingerprint string
	ProposalFingerprint    string
	CreatedAt              time.Time
	ExpiresAt              time.Time
	Consumed               bool
}

func (a Authority) validFor(t Transition, o Observation, now time.Time) error {
	if a.ExecutionID == "" {
		return ErrAuthorityMissing
	}
	if a.Consumed {
		return ErrAuthorityConsumed
	}
	if !a.ExpiresAt.IsZero() && !now.Before(a.ExpiresAt) {
		return ErrAuthorityExpired
	}
	if a.TransitionFingerprint != t.Fingerprint || a.ObservationFingerprint != o.Fingerprint || a.ProposalFingerprint != t.ProposalFingerprint {
		return ErrAuthorityMismatch
	}
	return nil
}

type ExecutionResult struct {
	Success bool
	Message string
}

type Executor interface {
	Execute(context.Context, Transition, Authority) ExecutionResult
}

type Verifier interface {
	Verify(context.Context, Transition, Authority) (Observation, error)
}

type StateStore struct {
	mu   sync.Mutex
	root string
}

func NewStateStore(root string) *StateStore { return &StateStore{root: root} }

func (s *StateStore) Root() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.root
}

func (s *StateStore) CompareAndSwap(expected, next string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root != expected {
		return ErrCASConflict
	}
	s.root = next
	return nil
}

type Runtime struct {
	mu                   sync.Mutex
	store                *StateStore
	policy               Policy
	clock                func() time.Time
	observation          *Observation
	proposal             *NormalizedProposal
	transition           *Transition
	decision             *GovernanceDecision
	authority            *Authority
	phase                Phase
	executionDone        chan struct{}
	executionCompletedAt time.Time
	verificationActive   bool
}

func NewRuntime(initialRoot string, policy Policy) *Runtime {
	if policy == nil {
		policy = AllowPolicy{}
	}
	return &Runtime{store: NewStateStore(initialRoot), policy: policy, clock: time.Now, phase: PhaseIdle}
}

func (r *Runtime) Root() string { return r.store.Root() }

func (r *Runtime) Phase() Phase {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.phase
}

func (r *Runtime) resetLifecycleLocked() {
	r.proposal, r.transition, r.decision, r.authority = nil, nil, nil, nil
	r.executionDone = nil
	r.executionCompletedAt = time.Time{}
	r.verificationActive = false
}

func (r *Runtime) Observe(o Observation) error {
	if err := o.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.phase == PhaseStarted || r.phase == PhaseRecovery {
		return ErrInvalidLifecycle
	}
	r.observation = &o
	r.resetLifecycleLocked()
	r.phase = PhaseObserved
	return nil
}

func (r *Runtime) Normalize(p Proposal) (NormalizedProposal, error) {
	n, err := Normalize(p)
	if err != nil {
		return NormalizedProposal{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.observation == nil || r.phase != PhaseObserved {
		return NormalizedProposal{}, ErrInvalidLifecycle
	}
	if n.Subject != r.observation.Subject {
		return NormalizedProposal{}, ErrInvalidProposal
	}
	r.proposal = &n
	r.phase = PhaseNormalized
	return n, nil
}

func (r *Runtime) Reconcile() (Transition, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.observation == nil || r.proposal == nil || r.phase != PhaseNormalized {
		return Transition{}, ErrInvalidLifecycle
	}
	t, err := Reconcile(*r.observation, *r.proposal)
	if err != nil {
		return Transition{}, err
	}
	r.transition = &t
	r.phase = PhaseReconciled
	return t, nil
}

func (r *Runtime) Govern() (GovernanceDecision, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.transition == nil || r.phase != PhaseReconciled {
		return GovernanceDecision{}, ErrInvalidLifecycle
	}
	d := r.policy.Govern(*r.transition)
	if d.TransitionFingerprint != r.transition.Fingerprint || d.ObservationFingerprint != r.transition.ObservationFingerprint || d.ProposalFingerprint != r.transition.ProposalFingerprint {
		return d, ErrGovernanceDenied
	}
	if !d.Allowed {
		if r.transition.Decision == DecisionNoop {
			return d, nil
		}
		return d, ErrGovernanceDenied
	}
	r.decision = &d
	r.phase = PhaseAuthorized
	return d, nil
}

func (r *Runtime) Reserve(lifetime time.Duration) (Authority, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.transition == nil || r.decision == nil || r.phase != PhaseAuthorized {
		return Authority{}, ErrInvalidLifecycle
	}
	if !r.decision.Allowed {
		return Authority{}, ErrGovernanceDenied
	}
	now := r.clock().UTC()
	a := Authority{ExecutionID: randomExecutionID(r.transition.Fingerprint, now), TransitionFingerprint: r.transition.Fingerprint, ObservationFingerprint: r.transition.ObservationFingerprint, ProposalFingerprint: r.transition.ProposalFingerprint, CreatedAt: now}
	if lifetime > 0 {
		a.ExpiresAt = now.Add(lifetime)
	}
	r.authority = &a
	r.phase = PhaseReserved
	return a, nil
}

func (r *Runtime) Start(ctx context.Context, e Executor) (ExecutionResult, error) {
	if e == nil {
		return ExecutionResult{}, ErrAuthorityMissing
	}
	r.mu.Lock()
	if r.phase != PhaseReserved || r.authority == nil || r.transition == nil || r.observation == nil {
		r.mu.Unlock()
		return ExecutionResult{}, ErrInvalidLifecycle
	}
	if err := r.authority.validFor(*r.transition, *r.observation, r.clock().UTC()); err != nil {
		r.mu.Unlock()
		return ExecutionResult{}, err
	}
	r.authority.Consumed = true
	a := *r.authority
	t := *r.transition
	r.executionDone = make(chan struct{})
	r.executionCompletedAt = time.Time{}
	r.verificationActive = false
	r.phase = PhaseStarted
	done := r.executionDone
	r.mu.Unlock()

	result := e.Execute(ctx, t, a)

	r.mu.Lock()
	if r.phase == PhaseStarted && r.executionDone == done {
		r.executionCompletedAt = r.clock().UTC()
		if !result.Success {
			r.phase = PhaseRecovery
		}
		close(done)
	}
	r.mu.Unlock()
	return result, nil
}

func (r *Runtime) Verify(ctx context.Context, v Verifier) error {
	if v == nil {
		return ErrVerificationFailed
	}
	r.mu.Lock()
	if r.phase != PhaseStarted || r.authority == nil || r.transition == nil || r.observation == nil || r.executionDone == nil || r.verificationActive {
		r.mu.Unlock()
		return ErrInvalidLifecycle
	}
	done := r.executionDone
	r.verificationActive = true
	r.mu.Unlock()

	select {
	case <-done:
	case <-ctx.Done():
		r.mu.Lock()
		if r.executionDone == done && r.verificationActive {
			r.verificationActive = false
		}
		r.mu.Unlock()
		return ctx.Err()
	}

	r.mu.Lock()
	if r.phase != PhaseStarted || r.authority == nil || r.transition == nil || r.observation == nil || r.executionDone != done {
		if r.executionDone == done && r.verificationActive {
			r.verificationActive = false
		}
		r.mu.Unlock()
		return ErrInvalidLifecycle
	}
	a, t := *r.authority, *r.transition
	completedAt := r.executionCompletedAt
	r.mu.Unlock()

	o, err := v.Verify(ctx, t, a)
	if err != nil {
		r.mu.Lock()
		if r.phase == PhaseStarted && r.executionDone == done && r.verificationActive {
			r.phase = PhaseRecovery
			r.verificationActive = false
		}
		r.mu.Unlock()
		return fmt.Errorf("%w: %v", ErrVerificationFailed, err)
	}
	if err := o.Validate(); err != nil {
		r.mu.Lock()
		if r.phase == PhaseStarted && r.executionDone == done && r.verificationActive {
			r.phase = PhaseRecovery
			r.verificationActive = false
		}
		r.mu.Unlock()
		return fmt.Errorf("%w: %v", ErrVerificationFailed, err)
	}
	if o.Subject != t.Subject || o.State != t.After || o.Fingerprint == t.ObservationFingerprint || !o.ObservedAt.After(completedAt) {
		r.mu.Lock()
		if r.phase == PhaseStarted && r.executionDone == done && r.verificationActive {
			r.phase = PhaseRecovery
			r.verificationActive = false
		}
		r.mu.Unlock()
		return ErrVerificationFailed
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.phase != PhaseStarted || r.executionDone != done || !r.verificationActive {
		return ErrInvalidLifecycle
	}
	r.verificationActive = false
	r.phase = PhaseVerified
	return nil
}

func (r *Runtime) Commit() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.phase != PhaseVerified || r.transition == nil {
		return ErrInvalidLifecycle
	}
	if err := r.store.CompareAndSwap(r.transition.Before, r.transition.After); err != nil {
		r.phase = PhaseRecovery
		return err
	}
	r.phase = PhaseCommitted
	return nil
}

func (r *Runtime) Recover(o Observation) error {
	if err := o.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.phase != PhaseRecovery || r.observation == nil || r.executionCompletedAt.IsZero() {
		return ErrInvalidLifecycle
	}
	if o.Subject != r.observation.Subject {
		return ErrInvalidObservation
	}
	if !o.ObservedAt.After(r.executionCompletedAt) {
		return ErrStaleEvidence
	}
	r.observation = &o
	r.resetLifecycleLocked()
	r.phase = PhaseObserved
	return nil
}

func fingerprint(v any) string {
	b, _ := json.Marshal(v)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func randomExecutionID(transitionID string, now time.Time) string {
	return fingerprint(struct {
		T string
		N int64
	}{transitionID, now.UnixNano()})
}
