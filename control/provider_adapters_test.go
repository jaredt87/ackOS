package control

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jaredt87/ackOS/kernel"
)

type adapterTestProvider struct {
	execute func(context.Context, ExecuteRequest) (Execution, error)
}

func (p adapterTestProvider) Observe(context.Context, ObserveRequest) (Observation, error) {
	return Observation{}, nil
}

func (p adapterTestProvider) Execute(ctx context.Context, req ExecuteRequest) (Execution, error) {
	if p.execute == nil {
		return Execution{ExecutionID: req.ExecutionID}, nil
	}
	return p.execute(ctx, req)
}

func (p adapterTestProvider) Verify(context.Context, VerifyRequest) (Verification, error) {
	return Verification{}, nil
}

func TestProviderExecutorCapturesExecutionErrors(t *testing.T) {
	sentinel := errors.New("provider rejected transition")
	cases := []struct {
		name      string
		provider  func(context.Context, ExecuteRequest) (Execution, error)
		beforeRun bool
		wantErr   error
	}{
		{
			name: "provider failure",
			provider: func(context.Context, ExecuteRequest) (Execution, error) {
				return Execution{}, sentinel
			},
			wantErr: sentinel,
		},
		{
			name: "mismatched execution ID",
			provider: func(context.Context, ExecuteRequest) (Execution, error) {
				return Execution{ExecutionID: "wrong"}, nil
			},
			wantErr: errors.New("provider returned mismatched execution ID"),
		},
		{
			name: "success leaves holder nil",
			provider: func(_ context.Context, req ExecuteRequest) (Execution, error) {
				return Execution{ExecutionID: req.ExecutionID}, nil
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var executionError error
			executor := NewProviderExecutor(
				adapterTestProvider{execute: tc.provider},
				ExecuteRequest{ExecutionID: "exec-1"},
				&Execution{},
				&executionError,
			)
			if tc.beforeRun {
				t.Fatal("beforeRun is not expected for an invoked adapter")
			}
			result := executor.Execute(context.Background(), kernel.Transition{}, kernel.Authority{}, kernel.Observation{})
			if tc.wantErr == nil {
				if executionError != nil {
					t.Fatalf("executionError = %v, want nil", executionError)
				}
				if !result.Success {
					t.Fatalf("result = %+v, want success", result)
				}
				return
			}
			if executionError == nil || executionError.Error() != tc.wantErr.Error() {
				t.Fatalf("executionError = %v, want %v", executionError, tc.wantErr)
			}
			if result.Success {
				t.Fatalf("result = %+v, want failure", result)
			}
		})
	}
}

func TestProviderExecutorHolderStaysNilWhenKernelDoesNotCallExecutor(t *testing.T) {
	var executionError error
	_ = NewProviderExecutor(
		adapterTestProvider{},
		ExecuteRequest{ExecutionID: "exec-1"},
		&Execution{},
		&executionError,
	)
	if executionError != nil {
		t.Fatalf("executionError = %v, want nil before executor invocation", executionError)
	}
}

func TestProviderExecutorCapturesCanceledContextBeforeAdmission(t *testing.T) {
	var executionError error
	executor := NewProviderExecutor(
		adapterTestProvider{},
		ExecuteRequest{ExecutionID: "exec-1"},
		&Execution{},
		&executionError,
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := executor.Execute(ctx, kernel.Transition{}, kernel.Authority{}, kernel.Observation{})
	if !errors.Is(executionError, context.Canceled) {
		t.Fatalf("executionError = %v, want context.Canceled", executionError)
	}
	if result.Success {
		t.Fatalf("result = %+v, want failure", result)
	}
}

func TestProviderExecutorCapturesGateAdmissionFailure(t *testing.T) {
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	var executeCalls atomic.Int32

	provider := adapterTestProvider{execute: func(_ context.Context, req ExecuteRequest) (Execution, error) {
		if executeCalls.Add(1) == 1 {
			close(firstEntered)
		}
		<-releaseFirst
		return Execution{ExecutionID: req.ExecutionID}, nil
	}}
	host, err := NewHost(kernel.NewRuntime("initial", kernel.AllowPolicy{}), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	invocation := &invocation{}
	adapter := host.providerAdapter(provider, invocation, "test")

	var firstError error
	firstExecutor := NewProviderExecutor(
		adapter,
		ExecuteRequest{ExecutionID: "exec-1"},
		&Execution{},
		&firstError,
	)
	firstDone := make(chan kernel.ExecutionResult, 1)
	go func() {
		firstDone <- firstExecutor.Execute(context.Background(), kernel.Transition{}, kernel.Authority{}, kernel.Observation{})
	}()

	<-firstEntered

	var secondError error
	secondExecutor := NewProviderExecutor(
		adapter,
		ExecuteRequest{ExecutionID: "exec-2"},
		&Execution{},
		&secondError,
	)
	secondCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	secondResult := secondExecutor.Execute(secondCtx, kernel.Transition{}, kernel.Authority{}, kernel.Observation{})

	if !errors.Is(secondError, ErrCallbackInFlight) {
		t.Fatalf("second executionError = %v, want ErrCallbackInFlight", secondError)
	}
	if secondResult.Success {
		t.Fatalf("second result = %+v, want failure", secondResult)
	}
	if got := executeCalls.Load(); got != 1 {
		t.Fatalf("provider Execute calls = %d, want 1", got)
	}

	close(releaseFirst)
	firstResult := <-firstDone
	if firstError != nil {
		t.Fatalf("first executionError = %v, want nil", firstError)
	}
	if !firstResult.Success {
		t.Fatalf("first result = %+v, want success", firstResult)
	}
}

func TestProviderExecutorCapturesProviderTimeoutAfterReturn(t *testing.T) {
	release := make(chan struct{})
	var executionError error
	executor := NewProviderExecutor(
		adapterTestProvider{
			execute: func(ctx context.Context, _ ExecuteRequest) (Execution, error) {
			<-release
			return Execution{}, ctx.Err()
		},
		},
		ExecuteRequest{ExecutionID: "exec-1"},
		&Execution{},
		&executionError,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	done := make(chan kernel.ExecutionResult, 1)
	go func() {
		done <- executor.Execute(ctx, kernel.Transition{}, kernel.Authority{}, kernel.Observation{})
	}()

	<-ctx.Done()
	close(release)

	result := <-done
	if !errors.Is(executionError, context.DeadlineExceeded) {
		t.Fatalf("executionError = %v, want context.DeadlineExceeded", executionError)
	}
	if result.Success {
		t.Fatalf("result = %+v, want failure", result)
	}
}
