package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/jaredt87/ackOS/control"
	"github.com/jaredt87/ackOS/kernel"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	ToolControl           = "ackos_control"
	maxAuthorityTTLMS     = int64((1<<63 - 1) / int64(time.Millisecond))
	defaultVerifyTimeout  = 30 * time.Second
	executionErrorMetaKey = "io.github.jaredt87/ackos/error"
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
type Observer interface {
	Observe(context.Context, string) (kernel.Observation, error)
}

type RecoveryObserver interface {
	Observe(context.Context, string) (kernel.Observation, error)
}

type Server struct {
	runtime          *kernel.Runtime
	executor         kernel.Executor
	provider         control.Provider
	verifier         kernel.Verifier
	observer         Observer
	recoveryObserver RecoveryObserver
	providerGate     chan struct{}
	verifyTimeout    time.Duration
}

type boundedVerifier struct {
	server   *Server
	verifier kernel.Verifier
}

type boundedExecutor struct {
	server           *Server
	executor         kernel.Executor
	executionOutcome chan<- bool
}

func NewServer(runtime *kernel.Runtime, executor kernel.Executor, verifier kernel.Verifier, observer Observer, recoveryObserver RecoveryObserver) (*Server, error) {
	return newServer(runtime, executor, verifier, observer, recoveryObserver, nil)
}

// NewServerWithProvider constructs the MCP server using a control provider.
// Provider adapters are created per control call so execution errors remain
// request-scoped until the MCP result is constructed.
func NewServerWithProvider(runtime *kernel.Runtime, provider control.Provider) (*Server, error) {
	if provider == nil {
		return nil, fmt.Errorf("provider is required")
	}
	observer := providerObserver{provider: provider}
	return newServer(runtime, nil, nil, observer, observer, provider)
}

func newServer(runtime *kernel.Runtime, executor kernel.Executor, verifier kernel.Verifier, observer Observer, recoveryObserver RecoveryObserver, provider control.Provider) (*Server, error) {
	if runtime == nil {
		return nil, fmt.Errorf("runtime is required")
	}
	if provider == nil {
		if executor == nil {
			return nil, fmt.Errorf("executor is required")
		}
		if verifier == nil {
			return nil, fmt.Errorf("independent verifier is required")
		}
	}
	if observer == nil {
		return nil, fmt.Errorf("provider observer is required")
	}
	if recoveryObserver == nil {
		return nil, fmt.Errorf("independent recovery observer is required")
	}
	providerGate := make(chan struct{}, 1)
	providerGate <- struct{}{}
	return &Server{
		runtime:          runtime,
		executor:         executor,
		provider:         provider,
		verifier:         verifier,
		observer:         observer,
		recoveryObserver: recoveryObserver,
		providerGate:     providerGate,
		verifyTimeout:    defaultVerifyTimeout,
	}, nil
}

type providerObserver struct {
	provider control.Provider
}

func (o providerObserver) Observe(ctx context.Context, subject string) (kernel.Observation, error) {
	result, err := o.provider.Observe(ctx, control.ObserveRequest{
		Target: control.ResourceRef{ID: subject},
	})
	if err != nil {
		return kernel.Observation{}, err
	}
	if result.Resource.ID != subject || result.Resource.Fingerprint == "" {
		return kernel.Observation{}, fmt.Errorf("provider returned invalid observation")
	}
	return kernel.NewObservation(
		result.Resource.ID,
		result.Resource.Fingerprint,
		result.Version,
		result.ObservedAt,
	)
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

func (s *Server) observeProvider(ctx context.Context, subject string) (kernel.Observation, error) {
	observeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.verifyTimeout)
	defer cancel()
	if err := s.acquireProvider(observeCtx); err != nil {
		return kernel.Observation{}, err
	}

	type result struct {
		observation kernel.Observation
		err         error
	}
	results := make(chan result, 1)
	go func() {
		defer s.releaseProvider()
		observation, err := s.observer.Observe(observeCtx, subject)
		results <- result{observation: observation, err: err}
	}()

	select {
	case result := <-results:
		return result.observation, result.err
	case <-observeCtx.Done():
		return kernel.Observation{}, fmt.Errorf("recovery observation timed out after %s: %w", s.verifyTimeout, observeCtx.Err())
	}
}
func (s *Server) observeRecovery(ctx context.Context, subject string) (kernel.Observation, error) {
	observeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.verifyTimeout)
	defer cancel()
	if err := s.acquireProvider(observeCtx); err != nil {
		return kernel.Observation{}, err
	}

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

func (v boundedVerifier) Verify(ctx context.Context, transition kernel.Transition, authority kernel.Authority, before kernel.Observation, execution kernel.ExecutionResult) (kernel.Observation, error) {
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
		verifier := v.server.verifier
		if v.verifier != nil {
			verifier = v.verifier
		}
		observation, err := verifier.Verify(verificationCtx, transition, authority, before, execution)
		results <- result{observation: observation, err: err}
	}()

	select {
	case result := <-results:
		return result.observation, result.err
	case <-verificationCtx.Done():
		return kernel.Observation{}, fmt.Errorf("independent verification timed out after %s: %w", v.server.verifyTimeout, verificationCtx.Err())
	}
}

func (e boundedExecutor) Execute(ctx context.Context, transition kernel.Transition, authority kernel.Authority, before kernel.Observation) kernel.ExecutionResult {
	executionCtx, cancel := context.WithTimeout(ctx, e.server.verifyTimeout)
	defer cancel()
	if err := e.server.acquireProvider(executionCtx); err != nil {
		e.reportExecutionOutcome(false)
		return kernel.ExecutionResult{Success: false, Message: fmt.Sprintf("executor admission failed: %v", err)}
	}

	results := make(chan kernel.ExecutionResult, 1)
	go func() {
		defer e.server.releaseProvider()
		// The MCP wrapper only transports the authorized snapshot to the remote
		// executor; the remote tool owns the mutating resource boundary and must
		// enforce Before itself.
		executor := e.server.executor
		if e.executor != nil {
			executor = e.executor
		}
		results <- executor.Execute(executionCtx, transition, authority, before)
	}()

	select {
	case result := <-results:
		e.reportExecutionOutcome(false)
		return result
	case <-executionCtx.Done():
		e.reportExecutionOutcome(true)
		return kernel.ExecutionResult{Success: false, Message: fmt.Sprintf("executor timed out after %s: %v", e.server.verifyTimeout, executionCtx.Err())}
	}
}

func (e boundedExecutor) reportExecutionOutcome(timedOut bool) {
	if e.executionOutcome != nil {
		e.executionOutcome <- timedOut
	}
}

func (s *Server) MCPServer() *mcpsdk.Server {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "ackOS", Version: "0.2.0"}, nil)
	outputSchema, err := jsonschema.For[ControlResponse](nil)
	if err != nil {
		panic(fmt.Sprintf("control output schema: %v", err))
	}
	mcpsdk.AddTool[ControlRequest, any](server, &mcpsdk.Tool{
		Name:         ToolControl,
		Description:  "Run one exact state transition through ackOS. The integration owns execution and independent verification; commit occurs only after verification.",
		OutputSchema: outputSchema,
	}, s.controlTool)
	return server
}

func (s *Server) control(ctx context.Context, _ *mcpsdk.CallToolRequest, in ControlRequest) (*mcpsdk.CallToolResult, ControlResponse, error) {
	return s.controlWithExecutionError(ctx, in, nil, nil)
}

func (s *Server) controlTool(ctx context.Context, req *mcpsdk.CallToolRequest, in ControlRequest) (*mcpsdk.CallToolResult, any, error) {
	var executionError error
	executionOutcomes := make(chan bool, 1)
	result, response, err := s.controlWithExecutionError(ctx, in, &executionError, executionOutcomes)
	if err == nil {
		return result, response, nil
	}
	if s.provider == nil || !strings.HasPrefix(err.Error(), "execution failed: ") {
		return nil, response, err
	}
	timedOut, completed := false, false
	select {
	case timedOut = <-executionOutcomes:
		completed = true
	default:
	}
	if !completed {
		timedOut = false
	}
	code := executionFailureCode(executionErrorValue(&executionError, completed && !timedOut), timedOut)
	return &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{
			&mcpsdk.TextContent{Text: err.Error()},
		},
		Meta: mcpsdk.Meta{
			executionErrorMetaKey: map[string]string{
				"code":    code,
				"message": err.Error(),
			},
		},
		IsError: true,
	}, nil, nil
}

func (s *Server) controlWithExecutionError(ctx context.Context, in ControlRequest, executionError *error, executionOutcomes chan<- bool) (*mcpsdk.CallToolResult, ControlResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, ControlResponse{}, err
	}
	if err := s.runtime.AcquireLifecycle(ctx); err != nil {
		return nil, ControlResponse{}, err
	}
	defer s.runtime.ReleaseLifecycle()

	if err := ctx.Err(); err != nil {
		return nil, ControlResponse{}, err
	}

	if in.AuthorityTTLMS < 0 || in.AuthorityTTLMS > maxAuthorityTTLMS {
		return nil, ControlResponse{}, fmt.Errorf("authority_ttl_ms must be between 0 and %d", maxAuthorityTTLMS)
	}

	var observation kernel.Observation
	var err error
	if s.runtime.Phase() == kernel.PhaseRecovery {
		recoverySubject, ok := s.runtime.RecoverySubject()
		if !ok || in.Subject != recoverySubject {
			return nil, ControlResponse{Phase: s.runtime.Phase(), Root: s.runtime.Root()}, fmt.Errorf("recovery subject must match the failed lifecycle")
		}
		observation, err = s.observeRecovery(ctx, recoverySubject)
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
		observation, err = s.observeProvider(ctx, in.Subject)
		if err != nil {
			return nil, ControlResponse{Phase: s.runtime.Phase(), Root: s.runtime.Root()}, err
		}
		if err := ctx.Err(); err != nil {
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
	if err := ctx.Err(); err != nil {
		return nil, ControlResponse{Phase: s.runtime.Phase(), Observation: observation, Transition: transition, Governance: governance}, err
	}

	authority, err := s.runtime.Reserve(time.Duration(in.AuthorityTTLMS) * time.Millisecond)
	if err != nil {
		return nil, ControlResponse{}, err
	}
	executor := s.executor
	verifier := s.verifier
	var providerExecution control.Execution
	if s.provider != nil {
		executor = control.NewProviderExecutor(
			s.provider,
			control.ExecuteRequest{
				ExecutionID: authority.ExecutionID,
				Target:      control.ResourceRef{ID: in.Subject},
				Payload:     []byte(in.DesiredState),
			},
			&providerExecution,
			executionError,
		)
	}
	execution, err := s.runtime.Start(ctx, boundedExecutor{server: s, executor: executor, executionOutcome: executionOutcomes})
	if err != nil {
		return nil, ControlResponse{Phase: s.runtime.Phase(), Observation: observation, Transition: transition, Governance: governance, Authority: authority}, explainRootDrift(err)
	}
	authority.Consumed = true
	if !execution.Success {
		return nil, ControlResponse{Phase: s.runtime.Phase(), Observation: observation, Transition: transition, Governance: governance, Authority: authority, Execution: execution}, fmt.Errorf("execution failed: %s", execution.Message)
	}

	// Verification survives MCP transport cancellation, but remains bounded by
	// an adapter-owned timeout. If it times out, Runtime.Verify receives an
	// ordinary verifier error and transitions the attempt into RECOVERY.
	if s.provider != nil {
		verifier = control.NewProviderVerifier(
			s.provider,
			control.VerifyRequest{
				Expected: control.ResourceRef{ID: in.Subject, Fingerprint: in.DesiredState},
			},
			&control.Verification{},
		)
	}
	if err := s.runtime.Verify(context.WithoutCancel(ctx), boundedVerifier{server: s, verifier: verifier}); err != nil {
		return nil, ControlResponse{Phase: s.runtime.Phase(), Observation: observation, Transition: transition, Governance: governance, Authority: authority, Execution: execution}, err
	}
	if err := s.runtime.Commit(); err != nil {
		return nil, ControlResponse{Phase: s.runtime.Phase(), Observation: observation, Transition: transition, Governance: governance, Authority: authority, Execution: execution, Verified: true, Root: s.runtime.Root()}, explainRootDrift(err)
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

func executionErrorValue(executionError *error, completed bool) error {
	if !completed || executionError == nil {
		return nil
	}
	return *executionError
}

func executionFailureCode(executionError error, timedOut bool) string {
	if timedOut {
		return "execution_failed"
	}
	if errors.Is(executionError, control.ErrStaleObservation) {
		return "stale_observation"
	}
	return "execution_failed"
}

func (s *Server) StreamableHTTPHandler() http.Handler {
	return mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server {
		return s.MCPServer()
	}, &mcpsdk.StreamableHTTPOptions{JSONResponse: true})
}

// explainRootDrift makes the kernel's fail-closed root conflict actionable for
// operators. Restarting re-baselines from current repository state without an
// authorization step; it is an explicit trust decision, not neutral recovery.
func explainRootDrift(err error) error {
	if errors.Is(err, kernel.ErrCASConflict) {
		return fmt.Errorf("kernel root no longer matches observed state: repository state changed outside ackOS since startup (or verification failed after a ref update); restart to re-baseline from current repository state, which trusts the current target blob without authorization: %w", err)
	}
	return err
}
