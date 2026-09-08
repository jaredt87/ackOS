package mcp

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/jaredt87/ackOS/kernel"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	ToolControl          = "ackos_control"
	maxAuthorityTTLMS    = int64((1<<63 - 1) / int64(time.Millisecond))
	defaultVerifyTimeout = 30 * time.Second
)

type ControlRequest struct {
	Subject        string `json:"subject" jsonschema:"the stable subject identity being controlled"`
	ObservedState  string `json:"observed_state" jsonschema:"the state observed before the requested transition"`
	DesiredState   string `json:"desired_state" jsonschema:"the exact desired state after the transition"`
	AuthorityTTLMS int64  `json:"authority_ttl_ms,omitempty" jsonschema:"optional authority lifetime in milliseconds; zero means no expiry"`
}

type ControlResponse struct {
	Phase       kernel.Phase              `json:"phase"`
	Observation kernel.Observation        `json:"observation"`
	Transition  kernel.Transition         `json:"transition"`
	Governance  kernel.GovernanceDecision `json:"governance"`
	Authority   kernel.Authority          `json:"authority"`
	Execution   kernel.ExecutionResult    `json:"execution"`
	Verified    bool                      `json:"verified"`
	Committed   bool                      `json:"committed"`
	Root        string                    `json:"root"`
}

// RecoveryObserver obtains fresh provider evidence after a failed attempt.
// Recovery must never manufacture a new observation from caller-supplied state.
type RecoveryObserver interface {
	Observe(context.Context, string) (kernel.Observation, error)
}

type Server struct {
	runtime          *kernel.Runtime
	executor         kernel.Executor
	verifier         kernel.Verifier
	recoveryObserver RecoveryObserver
	controlMu        sync.Mutex
	providerGate     chan struct{}
	verifyTimeout    time.Duration
}

type boundedVerifier struct {
	server *Server
}

func NewServer(runtime *kernel.Runtime, executor kernel.Executor, verifier kernel.Verifier, recoveryObserver RecoveryObserver) (*Server, error) {
	if runtime == nil {
		return nil, fmt.Errorf("runtime is required")
	}
	if executor == nil {
		return nil, fmt.Errorf("executor is required")
	}
	if verifier == nil {
		return nil, fmt.Errorf("independent verifier is required")
	}
	if recoveryObserver == nil {
		return nil, fmt.Errorf("independent recovery observer is required")
	}
	providerGate := make(chan struct{}, 1)
	providerGate <- struct{}{}
	return &Server{
		runtime:          runtime,
		executor:         executor,
		verifier:         verifier,
		recoveryObserver: recoveryObserver,
		providerGate:     providerGate,
		verifyTimeout:    defaultVerifyTimeout,
	}, nil
}

func (s *Server) acquireProvider(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.providerGate:
		return nil
	}
}

func (s *Server) releaseProvider() {
	s.providerGate <- struct{}{}
}

func (s *Server) observeRecovery(ctx context.Context, subject string) (kernel.Observation, error) {
	observeCtx, cancel := context.WithTimeout(ctx, s.verifyTimeout)
	defer cancel()

	type result struct {
		observation kernel.Observation
		err         error
	}
	results := make(chan result, 1)
	go func() {
		defer s.releaseProvider()
		observation, err := s.recoveryObserver.Observe(observeCtx, subject)
		results <- result{observation: observation, err: err}
	}()

	select {
	case result := <-results:
		return result.observation, result.err
	case <-observeCtx.Done():
		return kernel.Observation{}, fmt.Errorf("recovery observation timed out after %s: %w", s.verifyTimeout, observeCtx.Err())
	}
}

func (v boundedVerifier) Verify(ctx context.Context, transition kernel.Transition, authority kernel.Authority) (kernel.Observation, error) {
	verificationCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), v.server.verifyTimeout)
	defer cancel()
	if err := v.server.acquireProvider(verificationCtx); err != nil {
		return kernel.Observation{}, err
	}

	type result struct {
		observation kernel.Observation
		err         error
	}
	results := make(chan result, 1)
	go func() {
		defer v.server.releaseProvider()
		observation, err := v.server.verifier.Verify(verificationCtx, transition, authority)
		results <- result{observation: observation, err: err}
	}()

	select {
	case result := <-results:
		return result.observation, result.err
	case <-verificationCtx.Done():
		return kernel.Observation{}, fmt.Errorf("independent verification timed out after %s: %w", v.server.verifyTimeout, verificationCtx.Err())
	}
}

func (s *Server) MCPServer() *mcpsdk.Server {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "ackOS", Version: "0.2.0"}, nil)
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:        ToolControl,
		Description: "Run one exact state transition through ackOS. The integration owns execution and independent verification; commit occurs only after verification.",
	}, s.control)
	return server
}

func (s *Server) control(ctx context.Context, _ *mcpsdk.CallToolRequest, in ControlRequest) (*mcpsdk.CallToolResult, ControlResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, ControlResponse{}, err
	}

	s.controlMu.Lock()
	defer s.controlMu.Unlock()

	// A caller may have canceled while waiting for the shared V0 runtime.
	// Check admission again before creating observation/authority or executing.
	if err := ctx.Err(); err != nil {
		return nil, ControlResponse{}, err
	}

	if in.AuthorityTTLMS < 0 || in.AuthorityTTLMS > maxAuthorityTTLMS {
		return nil, ControlResponse{}, fmt.Errorf("authority_ttl_ms must be between 0 and %d", maxAuthorityTTLMS)
	}

	var observation kernel.Observation
	var err error
	if s.runtime.Phase() == kernel.PhaseRecovery {
		// Recovery evidence must come from the provider with its actual capture
		// time. Caller-supplied observed_state is never promoted to fresh evidence.
		if err := s.acquireProvider(ctx); err != nil {
			return nil, ControlResponse{Phase: s.runtime.Phase(), Root: s.runtime.Root()}, err
		}
		observation, err = s.observeRecovery(ctx, in.Subject)
		if err != nil {
			return nil, ControlResponse{Phase: s.runtime.Phase(), Root: s.runtime.Root()}, err
		}
		if err := ctx.Err(); err != nil {
			return nil, ControlResponse{Phase: s.runtime.Phase(), Observation: observation, Root: s.runtime.Root()}, err
		}
		if err := s.runtime.Recover(observation); err != nil {
			return nil, ControlResponse{Phase: s.runtime.Phase(), Observation: observation, Root: s.runtime.Root()}, err
		}
	} else {
		now := time.Now().UTC()
		observation, err = kernel.NewObservation(in.Subject, in.ObservedState, 0, now)
		if err != nil {
			return nil, ControlResponse{}, err
		}
	}

	if err := s.runtime.Observe(observation); err != nil {
		return nil, ControlResponse{}, err
	}

	if _, err := s.runtime.Normalize(kernel.Proposal{Subject: in.Subject, TargetState: in.DesiredState}); err != nil {
		return nil, ControlResponse{}, err
	}
	transition, err := s.runtime.Reconcile()
	if err != nil {
		return nil, ControlResponse{}, err
	}
	governance, err := s.runtime.Govern()
	if err != nil {
		return nil, ControlResponse{Phase: s.runtime.Phase(), Observation: observation, Transition: transition}, err
	}

	authority, err := s.runtime.Reserve(time.Duration(in.AuthorityTTLMS) * time.Millisecond)
	if err != nil {
		return nil, ControlResponse{}, err
	}
	execution, err := s.runtime.Start(ctx, s.executor)
	if err != nil {
		return nil, ControlResponse{Phase: s.runtime.Phase(), Observation: observation, Transition: transition, Governance: governance, Authority: authority}, err
	}
	authority.Consumed = true
	if !execution.Success {
		return nil, ControlResponse{Phase: s.runtime.Phase(), Observation: observation, Transition: transition, Governance: governance, Authority: authority, Execution: execution}, fmt.Errorf("execution failed: %s", execution.Message)
	}

	// Verification survives MCP transport cancellation, but remains bounded by
	// an adapter-owned timeout. If it times out, Runtime.Verify receives an
	// ordinary verifier error and transitions the attempt into RECOVERY.
	if err := s.runtime.Verify(context.Background(), boundedVerifier{server: s}); err != nil {
		return nil, ControlResponse{Phase: s.runtime.Phase(), Observation: observation, Transition: transition, Governance: governance, Authority: authority, Execution: execution}, err
	}
	if err := s.runtime.Commit(); err != nil {
		return nil, ControlResponse{Phase: s.runtime.Phase(), Observation: observation, Transition: transition, Governance: governance, Authority: authority, Execution: execution, Verified: true, Root: s.runtime.Root()}, err
	}

	return nil, ControlResponse{
		Phase:       s.runtime.Phase(),
		Observation: observation,
		Transition:  transition,
		Governance:  governance,
		Authority:   authority,
		Execution:   execution,
		Verified:    true,
		Committed:   true,
		Root:        s.runtime.Root(),
	}, nil
}

func (s *Server) StreamableHTTPHandler() http.Handler {
	return mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server {
		return s.MCPServer()
	}, &mcpsdk.StreamableHTTPOptions{JSONResponse: true})
}
