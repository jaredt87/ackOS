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

const ToolControl = "ackos_control"

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

type Server struct {
	runtime   *kernel.Runtime
	executor  kernel.Executor
	verifier  kernel.Verifier
	controlMu sync.Mutex
}

func NewServer(runtime *kernel.Runtime, executor kernel.Executor, verifier kernel.Verifier) (*Server, error) {
	if runtime == nil {
		return nil, fmt.Errorf("runtime is required")
	}
	if executor == nil {
		return nil, fmt.Errorf("executor is required")
	}
	if verifier == nil {
		return nil, fmt.Errorf("independent verifier is required")
	}
	return &Server{runtime: runtime, executor: executor, verifier: verifier}, nil
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
	s.controlMu.Lock()
	defer s.controlMu.Unlock()

	if in.AuthorityTTLMS < 0 {
		return nil, ControlResponse{}, fmt.Errorf("authority_ttl_ms must not be negative")
	}

	now := time.Now().UTC()
	observation, err := kernel.NewObservation(in.Subject, in.ObservedState, 0, now)
	if err != nil {
		return nil, ControlResponse{}, err
	}

	// A failed execution or verification leaves the runtime in RECOVERY. The
	// next control call must supply fresh post-failure evidence to re-enter the
	// normal lifecycle; recovery never reuses the prior transition authority.
	if s.runtime.Phase() == kernel.PhaseRecovery {
		if err := s.runtime.Recover(observation); err != nil {
			return nil, ControlResponse{Phase: s.runtime.Phase(), Observation: observation, Root: s.runtime.Root()}, err
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
	if !execution.Success {
		return nil, ControlResponse{Phase: s.runtime.Phase(), Observation: observation, Transition: transition, Governance: governance, Authority: authority, Execution: execution}, fmt.Errorf("execution failed: %s", execution.Message)
	}

	if err := s.runtime.Verify(ctx, s.verifier); err != nil {
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
