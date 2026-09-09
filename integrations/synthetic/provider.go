// Package synthetic provides a small external-resource implementation for
// exercising the ackOS provider boundary without a real infrastructure API.
package synthetic

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jaredt87/ackOS/kernel"
)

// Resource is the external system controlled by the synthetic provider.
// It is deliberately separate from kernel.StateStore so provider tests exercise
// an actual external-resource observation and execution boundary.
type Resource struct {
	mu      sync.Mutex
	subject string
	state   string
	version uint64
}

func NewResource(subject, state string) *Resource {
	return &Resource{subject: subject, state: state, version: 1}
}

func (r *Resource) Observe() (string, string, uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.subject, r.state, r.version
}

// Set mutates the external resource directly. Tests use this to model changes
// that occur outside ackOS between authorization and execution or verification.
func (r *Resource) Set(subject, state string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.subject = subject
	r.state = state
	r.version++
}

// Executor performs an exact transition against the external resource. It
// re-reads the resource immediately before mutation, providing the provider's
// domain-specific TOCTOU and identity guard.
type Executor struct {
	Resource *Resource
}

func (e Executor) Execute(ctx context.Context, t kernel.Transition, _ kernel.Authority) kernel.ExecutionResult {
	if e.Resource == nil {
		return kernel.ExecutionResult{Message: "synthetic resource is required"}
	}
	if err := ctx.Err(); err != nil {
		return kernel.ExecutionResult{Message: err.Error()}
	}

	subject, state, _ := e.Resource.Observe()
	if subject != t.Subject {
		return kernel.ExecutionResult{Message: fmt.Sprintf("resource subject mismatch: got %q, want %q", subject, t.Subject)}
	}
	if state != t.Before {
		return kernel.ExecutionResult{Message: fmt.Sprintf("resource state changed before execution: got %q, want %q", state, t.Before)}
	}

	e.Resource.mu.Lock()
	defer e.Resource.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return kernel.ExecutionResult{Message: err.Error()}
	}
	if e.Resource.subject != t.Subject || e.Resource.state != t.Before {
		return kernel.ExecutionResult{Message: "resource changed during execution precondition check"}
	}
	e.Resource.state = t.After
	e.Resource.version++
	return kernel.ExecutionResult{Success: true, Message: "synthetic resource transitioned"}
}

// Verifier independently reads the external resource. It contains no executor
// state and derives its observation only from the resource at verification time.
type Verifier struct {
	Resource *Resource
}

func (v Verifier) Verify(ctx context.Context, t kernel.Transition, _ kernel.Authority) (kernel.Observation, error) {
	if v.Resource == nil {
		return kernel.Observation{}, fmt.Errorf("synthetic resource is required")
	}
	if err := ctx.Err(); err != nil {
		return kernel.Observation{}, err
	}
	subject, state, version := v.Resource.Observe()
	return kernel.NewObservation(subject, state, version, time.Now().UTC())
}

// RecoveryObserver independently obtains fresh resource evidence after a
// failed execution, rather than manufacturing recovery state from the request.
type RecoveryObserver struct {
	Resource *Resource
}

func (o RecoveryObserver) Observe(ctx context.Context, subject string) (kernel.Observation, error) {
	if o.Resource == nil {
		return kernel.Observation{}, fmt.Errorf("synthetic resource is required")
	}
	if err := ctx.Err(); err != nil {
		return kernel.Observation{}, err
	}
	observedSubject, state, version := o.Resource.Observe()
	if observedSubject != subject {
		return kernel.Observation{}, fmt.Errorf("recovery subject mismatch: got %q, want %q", observedSubject, subject)
	}
	return kernel.NewObservation(observedSubject, state, version, time.Now().UTC())
}
