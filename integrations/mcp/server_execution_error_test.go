package mcp

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jaredt87/ackOS/kernel"
)

func TestControlExecutionFailureResponseAcrossRecoveryOutcomes(t *testing.T) {
	cases := []struct {
		name                string
		recoveryErr         error
		recoveryState       string
		expectedRecoveryErr string
	}{
		{name: "recovery succeeds"},
		{name: "recovery error surfaces", recoveryErr: errors.New("recovery unavailable"), expectedRecoveryErr: "recovery unavailable"},
		{name: "recovery conflict surfaces", recoveryState: "different", expectedRecoveryErr: "compare-and-swap conflict"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runtime := kernel.NewRuntime("initial", kernel.AllowPolicy{})
			executor := &testExecutor{}
			normalObserver := &testVerifier{}
			var recoveryObserveCalls atomic.Int64
			recoveryObserver := &testVerifier{observeErr: tc.recoveryErr, observeState: tc.recoveryState, observeCounter: &recoveryObserveCalls}
			server, err := NewServer(runtime, executor, normalObserver, normalObserver, recoveryObserver)
			if err != nil {
				t.Fatal(err)
			}

			_, first, err := server.control(context.Background(), nil, ControlRequest{
				Subject: "svc", ObservedState: "initial", DesiredState: "ready",
			})
			if err == nil {
				t.Fatal("expected execution failure")
			}
			if executor.calls != 1 {
				t.Fatalf("executor calls = %d, want 1 before recovery", executor.calls)
			}
			if err.Error() != "execution failed: executor rejected transition" {
				t.Fatalf("first err = %q, want original execution failure", err)
			}
			if first.Execution.Message != "executor rejected transition" {
				t.Fatalf("first execution = %+v, want original execution failure", first.Execution)
			}

			executor.success = true
			_, second, recoveryErr := server.control(context.Background(), nil, ControlRequest{
				Subject: "svc", ObservedState: "initial", DesiredState: "ready",
			})
			if recoveryObserveCalls.Load() != 1 {
				t.Fatalf("recovery observe calls = %d, want 1 after recovery attempt", recoveryObserveCalls.Load())
			}
			if tc.recoveryErr != nil || tc.recoveryState != "" {
				if recoveryErr == nil {
					t.Fatal("expected recovery error")
				}
				if !strings.Contains(recoveryErr.Error(), tc.expectedRecoveryErr) {
					t.Fatalf("recovery err = %q, want substring %q", recoveryErr, tc.expectedRecoveryErr)
				}
				if executor.calls != 1 {
					t.Fatalf("executor calls = %d, want 1 after recovery failure", executor.calls)
				}
				return
			}
			if recoveryErr != nil {
				t.Fatal(recoveryErr)
			}
			if !second.Committed {
				t.Fatalf("second response = %+v, want recovery success", second)
			}

		})
	}
}
