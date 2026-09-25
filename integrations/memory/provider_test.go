package memory

import (
	"context"
	"testing"
	"time"

	"github.com/jaredt87/ackOS/control"
)

func TestProviderIsIndependentOfGit(t *testing.T) {
	resource, _ := NewResource("resource-a", "state:v1")
	provider, _ := NewProvider(resource)

	observation, err := provider.Observe(context.Background(), control.ObserveRequest{
		Target: control.ResourceRef{ID: "resource-a", Fingerprint: "ignored"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if observation.Resource.Fingerprint != "state:v1" {
		t.Fatalf("fingerprint = %q", observation.Resource.Fingerprint)
	}

	execution, err := provider.Execute(context.Background(), control.ExecuteRequest{
		ExecutionID: "execution-1",
		Target:      observation.Resource,
		Payload:     []byte("state:v2"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if execution.ExecutionID != "execution-1" {
		t.Fatalf("execution ID = %q", execution.ExecutionID)
	}

	verification, err := provider.Verify(context.Background(), control.VerifyRequest{
		ExecutionID: "execution-1",
		Expected:    control.ResourceRef{ID: "resource-a", Fingerprint: "state:v2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if verification.Resource.Fingerprint != "state:v2" {
		t.Fatalf("verification fingerprint = %q", verification.Resource.Fingerprint)
	}
}

func TestProviderVerifyTimestampFollowsLockedSnapshot(t *testing.T) {
	resource, err := NewResource("resource-a", "state:v1")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := NewProvider(resource)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Execute(context.Background(), control.ExecuteRequest{
		ExecutionID: "execution-1",
		Target:      control.ResourceRef{ID: "resource-a", Fingerprint: "state:v1"},
		Payload:     []byte("state:v2"),
	}); err != nil {
		t.Fatal(err)
	}

	resource.mu.Lock()
	setDone := make(chan struct{})
	go func() {
		resource.Set("state:v3")
		close(setDone)
	}()
	resource.mu.Unlock()

	select {
	case <-setDone:
	case <-time.After(time.Second):
		t.Fatal("Set did not complete after resource lock was released")
	}

	verification, err := provider.Verify(context.Background(), control.VerifyRequest{
		ExecutionID: "execution-1",
		Expected:    control.ResourceRef{ID: "resource-a", Fingerprint: "state:v3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if verification.Resource.Fingerprint != "state:v3" {
		t.Fatalf("fingerprint = %q, want state:v3", verification.Resource.Fingerprint)
	}
	if verification.VerifiedAt.IsZero() {
		t.Fatal("VerifiedAt is zero")
	}
}
