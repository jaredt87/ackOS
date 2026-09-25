package control

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ErrCallbackInFlight is returned when a provider callback cannot be admitted
// because a prior callback against that same provider is still running.
var ErrCallbackInFlight = errors.New("control: provider callback still in flight")

type providerGate struct {
	mu    sync.Mutex
	busy  bool
	owner string
}

func (g *providerGate) tryEnter(owner string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.busy {
		return false
	}
	g.busy = true
	g.owner = owner
	return true
}

func (g *providerGate) leave() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.busy = false
	g.owner = ""
}

func (g *providerGate) currentOwner() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.owner
}

type providerGates struct {
	mu    sync.Mutex
	gates map[string]*providerGate
}

func (g *providerGates) forProvider(name string) *providerGate {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.gates == nil {
		g.gates = make(map[string]*providerGate)
	}
	gate, ok := g.gates[name]
	if !ok {
		gate = &providerGate{}
		g.gates[name] = gate
	}
	return gate
}

// callProvider bounds the Host's wait on a provider callback by ctx while
// keeping the provider admission gate occupied until the callback actually
// returns. A timed-out, non-cooperative callback therefore cannot overlap a
// later callback for the same provider.
func (h *Host) callProvider(ctx context.Context, providerName, ownerID string, fn func() error) error {
	gate := h.gates.forProvider(providerName)
	if !gate.tryEnter(ownerID) {
		return fmt.Errorf("%w: provider %q (held by %s)", ErrCallbackInFlight, providerName, gate.currentOwner())
	}

	done := make(chan error, 1)
	go func() {
		defer gate.leave()
		done <- fn()
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		// Do not release the gate here. The callback goroutine owns release
		// until it actually returns.
		return ctx.Err()
	}
}
