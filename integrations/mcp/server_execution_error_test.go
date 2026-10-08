package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/jaredt87/ackOS/kernel"
)

func TestControlExecutionFailureResponseRemainsOriginalAcrossRecoveryOutcomes(t *testing.T) {
	cases := []struct {
		name          string
		recoveryErr   error
		recoveryState string
	}{
		{name: "recovery succeeds"},
		{name: "recovery errors", recoveryErr: errors.New("recovery unavailable")},
		{name: "recovery observes different state", recoveryState: "different"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runtime := kernel.NewRuntime("initial", kernel.AllowPolicy{})
			executor := &testExecutor{}
			verifier := &testVerifier{observeErr: tc.recoveryErr, observeState: tc.recoveryState}
			server := newTestServer(t, runtime, executor, verifier)

			_, first, err := server.control(context.Background(), nil, ControlRequest{
				Subject: "svc", ObservedState: "initial", DesiredState: "ready",
			})
			if err == nil {
				t.Fatal("expected execution failure")
			}
			if got := err.Error(); got != "execution failed: executor rejected transition" {
				t.Fatalf("err = %q, want original execution failure", got)
			}
			if first.Execution.Message != "executor rejected transition" {
				t.Fatalf("execution = %+v, want original execution failure", first.Execution)
			}

			executor.success = true
			_, second, recoveryErr := server.control(context.Background(), nil, ControlRequest{
				Subject: "svc", ObservedState: "initial", DesiredState: "ready",
			})
			if tc.recoveryErr != nil {
				if recoveryErr == nil {
					t.Fatal("expected recovery error")
				}
				if second.Execution.Message != "" {
					t.Fatalf("second execution = %+v, want no relabeling of first failure", second.Execution)
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
