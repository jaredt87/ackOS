package control

import (
	"context"
	"testing"
	"time"

	"github.com/jaredt87/ackOS/kernel"
)

type timeoutRaceProvider struct {
	release <-chan struct{}
}

func (p timeoutRaceProvider) Observe(context.Context, ObserveRequest) (Observation, error) {
	<-p.release
	return Observation{
		Resource: ResourceRef{ID: "resource-a", Fingerprint: "initial"},
		Version:  1,
	}, nil
}

func (p timeoutRaceProvider) Execute(_ context.Context, req ExecuteRequest) (Execution, error) {
	<-p.release
	return Execution{ExecutionID: req.ExecutionID}, nil
}

func (p timeoutRaceProvider) Verify(<-chan struct{}, VerifyRequest) (Verification, error) {
	panic("unreachable")
}

func TestInvocationProviderReturnsBeforeReadingTimedOutObserveResult(t *testing.T) {
	for range 100 {
		release := make(chan struct{})
		provider := timeoutRaceProvider{release: release}
		host, err := NewHost(kernel.NewRuntime("initial", kernel.AllowPolicy{}), time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		inv := &invocation{}
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		done := make(chan struct{})
		go func() {
			_, _ = (invocationProvider{
				host:         host,
				invocation:   inv,
				providerName: "test",
				provider:     provider,
			}).Observe(ctx, ObserveRequest{Target: ResourceRef{ID: "resource-a"}})
			close(done)
		}()

		<-ctx.Done()
		close(release)
		<-done
		cancel()
	}
}

func TestInvocationProviderReturnsBeforeReadingTimedOutExecuteResult(t *testing.T) {
	for range 100 {
		release := make(chan struct{})
		provider := timeoutRaceProvider{release: release}
		host, err := NewHost(kernel.NewRuntime("initial", kernel.AllowPolicy{}), time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		inv := &invocation{}
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		done := make(chan struct{})
		go func() {
			_, _ = (invocationProvider{
				host:         host,
				invocation:   inv,
				providerName: "test",
				provider:     provider,
			}).Execute(ctx, ExecuteRequest{ExecutionID: "execution-1"})
			close(done)
		}()

		<-ctx.Done()
		close(release)
		<-done
		cancel()
	}
}
