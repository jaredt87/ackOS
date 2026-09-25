package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/jaredt87/ackOS/control"
)

// Resource is intentionally domain-neutral. Its state is represented only by
// an opaque fingerprint; no provider-specific interpretation is required.
type Resource struct {
	mu          sync.Mutex
	id          string
	fingerprint string
	executionID string
	version     uint64
}

func NewResource(id, fingerprint string) (*Resource, error) {
	if id == "" || fingerprint == "" {
		return nil, fmt.Errorf("id and fingerprint are required")
	}
	return &Resource{id: id, fingerprint: fingerprint, version: 1}, nil
}

var _ control.Provider = (*Provider)(nil)

type Provider struct {
	resource *Resource
}

func NewProvider(resource *Resource) (*Provider, error) {
	if resource == nil {
		return nil, fmt.Errorf("resource is required")
	}
	return &Provider{resource: resource}, nil
}

func (p *Provider) Observe(ctx context.Context, req control.ObserveRequest) (control.Observation, error) {
	if err := ctx.Err(); err != nil {
		return control.Observation{}, err
	}
	p.resource.mu.Lock()
	id, fingerprint, version := p.resource.id, p.resource.fingerprint, p.resource.version
	observedAt := time.Now().UTC()
	p.resource.mu.Unlock()
	if req.Target.ID != id {
		return control.Observation{}, fmt.Errorf("resource ID mismatch")
	}
	return control.Observation{
		Resource:   control.ResourceRef{ID: id, Fingerprint: fingerprint},
		Evidence:   mustEvidence(id, fingerprint, version),
		ObservedAt: observedAt,
	}, nil
}

func (p *Provider) Execute(ctx context.Context, req control.ExecuteRequest) (control.Execution, error) {
	if req.ExecutionID == "" {
		return control.Execution{}, fmt.Errorf("execution ID is required")
	}
	if err := ctx.Err(); err != nil {
		return control.Execution{}, err
	}
	p.resource.mu.Lock()
	defer p.resource.mu.Unlock()
	if p.resource.id != req.Target.ID || p.resource.fingerprint != req.Target.Fingerprint {
		return control.Execution{}, fmt.Errorf("resource changed before execution")
	}
	if string(req.Payload) == "" {
		return control.Execution{}, fmt.Errorf("payload is required")
	}
	p.resource.fingerprint = string(req.Payload)
	p.resource.version++
	p.resource.executionID = req.ExecutionID
	return control.Execution{ExecutionID: req.ExecutionID, Evidence: mustEvidence(p.resource.id, p.resource.fingerprint, p.resource.version)}, nil
}

func (p *Provider) Verify(ctx context.Context, req control.VerifyRequest) (control.Verification, error) {
	if req.ExecutionID == "" {
		return control.Verification{}, fmt.Errorf("execution ID is required")
	}
	if err := ctx.Err(); err != nil {
		return control.Verification{}, err
	}
	p.resource.mu.Lock()
	id, fingerprint, executionID, version := p.resource.id, p.resource.fingerprint, p.resource.executionID, p.resource.version
	verifiedAt := time.Now().UTC()
	p.resource.mu.Unlock()
	if executionID != req.ExecutionID {
		return control.Verification{}, fmt.Errorf("execution is not bound to this resource state")
	}
	if id != req.Expected.ID || fingerprint != req.Expected.Fingerprint {
		return control.Verification{}, fmt.Errorf("resource does not match expected state")
	}
	return control.Verification{
		Resource:   control.ResourceRef{ID: id, Fingerprint: fingerprint},
		Evidence:   mustEvidence(id, fingerprint, version),
		VerifiedAt: verifiedAt,
	}, nil
}

func (r *Resource) Set(fingerprint string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fingerprint = fingerprint
	r.version++
	r.executionID = ""
}

func mustEvidence(id, fingerprint string, version uint64) []byte {
	b, _ := json.Marshal(struct {
		ID          string
		Fingerprint string
		Version     uint64
	}{id, fingerprint, version})
	return b
}
