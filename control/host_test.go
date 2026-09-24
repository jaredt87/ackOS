package control

import (
	"context"
	"testing"
	"time"

	"github.com/jaredt87/ackOS/integrations/memory"
	"github.com/jaredt87/ackOS/kernel"
)

func TestHostMemoryProviderLifecycle(t *testing.T) {
	resource, err := memory.NewResource("resource-a", "initial")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := memory.NewProvider(resource)
	if err != nil {
		t.Fatal(err)
	}
	host, err := NewHost(kernel.NewRuntime("initial", kernel.AllowPolicy{}), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Register("memory", provider); err != nil {
		t.Fatal(err)
	}

	result, err := host.Control(context.Background(), "memory", ControlRequest{
		Target:  ResourceRef{ID: "resource-a", Fingerprint: "initial"},
		Desired: ResourceRef{ID: "resource-a", Fingerprint: "running"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Authority.ExecutionID == "" || result.Execution.ExecutionID != result.Authority.ExecutionID {
		t.Fatal("execution was not bound to host-issued authority")
	}
	if result.Verification.Resource.Fingerprint != "running" {
		t.Fatalf("verification fingerprint = %q, want running", result.Verification.Resource.Fingerprint)
	}
	if got := result.Transition.Before; got != "initial" {
		t.Fatalf("transition before = %q", got)
	}
}

func TestHostRejectsResourceSubstitution(t *testing.T) {
	resource, _ := memory.NewResource("resource-a", "initial")
	provider, _ := memory.NewProvider(resource)
	host, _ := NewHost(kernel.NewRuntime("initial", kernel.AllowPolicy{}), time.Second)
	_ = host.Register("memory", provider)

	resource.Set("other")
	_, err := host.Control(context.Background(), "memory", ControlRequest{
		Target:  ResourceRef{ID: "resource-a", Fingerprint: "initial"},
		Desired: ResourceRef{ID: "resource-a", Fingerprint: "running"},
	})
	if err == nil {
		t.Fatal("control unexpectedly succeeded after resource mutation")
	}
}

func TestHostDoesNotInterpretFingerprint(t *testing.T) {
	resource, _ := memory.NewResource("resource-a", "opaque::provider-state")
	provider, _ := memory.NewProvider(resource)
	host, _ := NewHost(kernel.NewRuntime("opaque::provider-state", kernel.AllowPolicy{}), time.Second)
	_ = host.Register("memory", provider)

	_, err := host.Control(context.Background(), "memory", ControlRequest{
		Target:  ResourceRef{ID: "resource-a", Fingerprint: "opaque::provider-state"},
		Desired: ResourceRef{ID: "resource-a", Fingerprint: "another::provider-state"},
	})
	if err != nil {
		t.Fatal(err)
	}
}
