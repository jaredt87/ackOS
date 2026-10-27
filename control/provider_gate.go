package control

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// ErrCallbackInFlight is returned when a provider callback cannot be admitted
// because a prior callback against that same provider is still running.
var ErrCallbackInFlight = errors.New("control: provider callback still in flight")

// executionResultGrace lets a side-effecting Execute callback report its final
// outcome if the request deadline expires while it is recovering an ambiguous
// commit result. File providers bound that recovery read to five seconds.
const executionResultGrace = 6 * time.Second

type providerGate struct {
	mu    sync.Mutex
	busy  atomic.Bool
	owner string
}

func (g *providerGate) tryEnter(owner string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.busy.Load() {
		return false
	}
	g.busy.Store(true)
	g.owner = owner
	return true
}

func (g *providerGate) leave() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.busy.Store(false)
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
func (h *Host) callProvider(ctx context.Context, providerName, ownerID string, fn func() error) (error, bool) {
	return h.callProviderWithGrace(ctx, providerName, ownerID, 0, fn)
}

// callProviderWithGrace optionally waits a bounded interval after cancellation
// for an in-flight Execute callback to report whether its side effect committed.
// Other callbacks use callProvider and retain the original immediate timeout.
func (h *Host) callProviderWithGrace(ctx context.Context, providerName, ownerID string, grace time.Duration, fn func() error) (error, bool) {
	gate := h.gates.forProvider(providerName)
	if !gate.tryEnter(ownerID) {
		return fmt.Errorf("%w: provider %q (held by %s)", ErrCallbackInFlight, providerName, gate.currentOwner()), false
	}

	done := make(chan error, 1)
	var completed atomic.Bool
	go func() {
		err := fn()
		gate.leave()
		completed.Store(true)
		done <- err
	}()

	select {
	case err := <-done:
		return err, true
	case <-ctx.Done():
		// If the callback completed before cancellation won the select, preserve
		// its result. Otherwise the callback remains admitted until it returns.
		if completed.Load() {
			return <-done, true
		}
		if grace <= 0 {
			return ctx.Err(), true
		}
		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case err := <-done:
			return err, true
		case <-timer.C:
			// Avoid discarding a result that completed as the grace timer fired.
			if completed.Load() {
				return <-done, true
			}
			return ctx.Err(), true
		}
	}
}
