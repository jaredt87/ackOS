package control_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jaredt87/ackOS/control"
	"github.com/jaredt87/ackOS/integrations/memory"
	"github.com/jaredt87/ackOS/kernel"
)

func TestHostRejectsChangedExpectedObservationBeforeExecution(t *testing.T) {
	resource, err := memory.NewResource("resource-a", "initial")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := memory.NewProvider(resource)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := provider.Observe(context.Background(), control.ObserveRequest{
		Target: control.ResourceRef{ID: "resource-a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resource.Set("changed-after-inspection")

	runtime := kernel.NewRuntime("initial", kernel.AllowPolicy{})
	host, err := control.NewHost(runtime, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Register("memory", provider); err != nil {
		t.Fatal(err)
	}

	_, err = host.Control(context.Background(), "memory", control.ControlRequest{
		Target:              control.ResourceRef{ID: "resource-a"},
		Desired:             control.ResourceRef{ID: "resource-a", Fingerprint: "desired"},
		Payload:             []byte("desired"),
		ExpectedObservation: &expected,
	})
	if !errors.Is(err, control.ErrStaleObservation) {
		t.Fatalf("Control() error = %v, want ErrStaleObservation", err)
	}
	if got := runtime.Phase(); got != kernel.PhaseIdle {
		t.Fatalf("runtime phase = %s, want IDLE because execution must not start", got)
	}
	if got := runtime.Root(); got != "initial" {
		t.Fatalf("runtime root = %q, want initial", got)
	}
}
