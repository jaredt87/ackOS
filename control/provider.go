package control

import (
	"context"
	"time"
)

// ResourceRef identifies an opaque provider resource state.
// Fingerprint is compared for equality only; its contents have no meaning to
// the control layer.
type ResourceRef struct {
	ID          string
	Fingerprint string
}

func (r ResourceRef) valid() bool {
	return r.ID != "" && r.Fingerprint != ""
}

type ObserveRequest struct {
	Target ResourceRef
}

type Observation struct {
	Resource  ResourceRef
	Evidence  []byte
	ObservedAt time.Time
}

type ExecuteRequest struct {
	ExecutionID string
	Target      ResourceRef
	Payload     []byte
}

type Execution struct {
	ExecutionID string
	Evidence    []byte
}

type VerifyRequest struct {
	ExecutionID string
	Expected    ResourceRef
}

type Verification struct {
	Resource   ResourceRef
	Evidence   []byte
	VerifiedAt time.Time
}

type Provider interface {
	Observe(context.Context, ObserveRequest) (Observation, error)
	Execute(context.Context, ExecuteRequest) (Execution, error)
	Verify(context.Context, VerifyRequest) (Verification, error)
}
