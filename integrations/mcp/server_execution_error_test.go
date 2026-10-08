package mcp

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jaredt87/ackOS/control"
	"github.com/jaredt87/ackOS/kernel"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

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


type typedExecutionProvider struct {
	execute func(context.Context, control.ExecuteRequest) (control.Execution, error)
}

func (p typedExecutionProvider) Observe(context.Context, control.ObserveRequest) (control.Observation, error) {
	return control.Observation{
		Resource: control.ResourceRef{ID: "svc", Fingerprint: "initial"},
		Version:  1,
		ObservedAt: time.Now().UTC(),
	}, nil
}

func (p typedExecutionProvider) Execute(ctx context.Context, req control.ExecuteRequest) (control.Execution, error) {
	return p.execute(ctx, req)
}

func (p typedExecutionProvider) Verify(context.Context, control.VerifyRequest) (control.Verification, error) {
	return control.Verification{
		Resource:   control.ResourceRef{ID: "svc", Fingerprint: "ready"},
		Version:    1,
		VerifiedAt: time.Now().UTC(),
	}, nil
}

func callProviderControl(t *testing.T, server *Server) *mcpsdk.CallToolResult {
	t.Helper()
	mcpServer := server.MCPServer()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "ackos-test-client", Version: "test"}, nil)
	clientTransport, serverTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := mcpServer.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	clientSession, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	result, err := clientSession.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name: ToolControl,
		Arguments: map[string]any{
			"subject":        "svc",
			"observed_state": "ignored",
			"desired_state":  "ready",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func assertStructuredExecutionError(t *testing.T, result *mcpsdk.CallToolResult, code string) {
	t.Helper()
	if !result.IsError {
		t.Fatalf("result = %+v, want tool error", result)
	}
	if len(result.Content) != 1 {
		t.Fatalf("content = %+v, want one item", result.Content)
	}
	textContent, ok := result.Content[0].(*mcpsdk.TextContent)
	if !ok {
		t.Fatalf("content[0] = %T, want *mcp.TextContent", result.Content[0])
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structured content = %#v, want object", result.StructuredContent)
	}
	errorValue, ok := structured["error"].(map[string]any)
	if !ok {
		t.Fatalf("structured error = %#v, want object", structured["error"])
	}
	gotCode, ok := errorValue["code"].(string)
	if !ok || gotCode != code {
		t.Fatalf("structured error code = %#v, want %q", errorValue["code"], code)
	}
	message, ok := errorValue["message"].(string)
	if !ok || message != textContent.Text {
		t.Fatalf("structured error message = %#v, want text %q", errorValue["message"], textContent.Text)
	}
}

func TestControlExecutionFailureStructuredError(t *testing.T) {
	cases := []struct {
		name        string
		provider    typedExecutionProvider
		wantCode    string
		wantMessage string
	}{
		{
			name: "stale observation",
			provider: typedExecutionProvider{
				execute: func(context.Context, control.ExecuteRequest) (control.Execution, error) {
					return control.Execution{}, control.ErrStaleObservation
				},
			},
			wantCode:    "stale_observation",
			wantMessage: "execution failed: stale pre-execution observation",
		},
		{
			name: "provider failure",
			provider: typedExecutionProvider{
				execute: func(context.Context, control.ExecuteRequest) (control.Execution, error) {
					return control.Execution{}, errors.New("provider rejected transition")
				},
			},
			wantCode:    "execution_failed",
			wantMessage: "execution failed: provider rejected transition",
		},
		{
			name: "mismatched execution ID",
			provider: typedExecutionProvider{
				execute: func(context.Context, control.ExecuteRequest) (control.Execution, error) {
					return control.Execution{ExecutionID: "wrong"}, nil
				},
			},
			wantCode:    "execution_failed",
			wantMessage: "execution failed: provider returned mismatched execution ID",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runtime := kernel.NewRuntime("initial", kernel.AllowPolicy{})
			server, err := NewServerWithProvider(runtime, tc.provider)
			if err != nil {
				t.Fatal(err)
			}
			result := callProviderControl(t, server)
			assertStructuredExecutionError(t, result, tc.wantCode)
			structured := result.StructuredContent.(map[string]any)
			errorValue := structured["error"].(map[string]any)
			if got := errorValue["message"]; got != tc.wantMessage {
				t.Fatalf("structured error message = %q, want %q", got, tc.wantMessage)
			}
		})
	}
}

func TestControlExecutionTimeoutStructuredError(t *testing.T) {
	release := make(chan struct{})
	server, err := NewServerWithProvider(kernel.NewRuntime("initial", kernel.AllowPolicy{}), typedExecutionProvider{
		execute: func(context.Context, control.ExecuteRequest) (control.Execution, error) {
			<-release
			return control.Execution{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server.verifyTimeout = 10 * time.Millisecond

	result := callProviderControl(t, server)
	close(release)
	assertStructuredExecutionError(t, result, "execution_failed")
	structured := result.StructuredContent.(map[string]any)
	errorValue := structured["error"].(map[string]any)
	if message := errorValue["message"].(string); !strings.Contains(message, "executor timed out after") {
		t.Fatalf("structured error message = %q, want executor timeout", message)
	}
}
