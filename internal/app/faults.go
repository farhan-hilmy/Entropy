package app

import (
	"context"
	"errors"
	"fmt"

	"entropy/internal/faults"
	"entropy/internal/toxiproxy"
)

// StartFault applies a fault to a dependency for spec.DurationSec seconds.
func (a *App) StartFault(ctx context.Context, dependency string, spec faults.Spec) (faults.Fault, error) {
	deps, err := a.store.Dependencies(ctx)
	if err != nil {
		return faults.Fault{}, err
	}
	known := false
	for _, d := range deps {
		known = known || (d.Name == dependency && !d.Missing)
	}
	if !known {
		return faults.Fault{}, fmt.Errorf("%w: no dependency %s in the config file", ErrNotFound, dependency)
	}
	if a.isVerifying(dependency) { // its route check opens and closes the proxy
		return faults.Fault{}, ErrVerifying
	}
	f, err := a.faults.Start(ctx, dependency, spec)
	a.CheckSoon()
	return f, err
}

// StopFault removes a dependency's fault now.
func (a *App) StopFault(dependency string) error {
	err := a.faults.Stop(dependency)
	a.CheckSoon()
	if errors.Is(err, faults.ErrNoFault) {
		return fmt.Errorf("%w: %s has no fault", ErrNotFound, dependency)
	}
	return err
}

// ClearLeftovers enables every proxy and removes every toxic, for faults a
// previous Entropy left behind (e.g. it was killed mid-fault). If Toxiproxy
// isn't reachable, the first check round that reaches it does it instead.
func (a *App) ClearLeftovers(ctx context.Context) error {
	err := a.client().Reset(ctx)
	a.mu.Lock()
	a.leftoversPending = err != nil
	a.mu.Unlock()
	return err
}

// clearPendingLeftovers does the startup clear that failed, now that tp
// answered; reset is true if it did. A fault started since then is wiped too,
// but Reapply, later in the same round, puts it back.
func (a *App) clearPendingLeftovers(ctx context.Context, tp *toxiproxy.Client) (reset bool) {
	a.mu.Lock()
	pending := a.leftoversPending
	a.mu.Unlock()
	if !pending {
		return false
	}
	if err := tp.Reset(ctx); err != nil {
		a.log.Warn("could not clear leftover faults; trying again next round", "err", err)
		return false
	}
	a.mu.Lock()
	a.leftoversPending = false
	a.mu.Unlock()
	a.log.Info("cleared leftover faults (Toxiproxy was unreachable at startup)")
	return true
}

// StopAllFaults aborts the running experiment, ends every fault Entropy
// started, then enables every proxy and removes every toxic, including faults
// added outside Entropy.
func (a *App) StopAllFaults(ctx context.Context) error {
	a.abortActiveRun("Stop all faults was pressed") // the kill switch also ends the run
	stopErr := a.faults.StopAll()
	err := a.client().Reset(ctx)
	a.CheckSoon()
	if err != nil {
		return err // Reset failing says more than which removals are still pending
	}
	a.mu.Lock()
	a.leftoversPending = false // this reset cleared them too
	a.mu.Unlock()
	if stopErr != nil {
		// Reset cleared Toxiproxy, so the pending removals will succeed (or find nothing) on their next try.
		a.log.Warn("faults were still being removed when Toxiproxy was reset", "err", stopErr)
	}
	return nil
}
