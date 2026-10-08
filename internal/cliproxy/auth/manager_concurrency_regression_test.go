package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"
)

// cloningSelector mimics selectors that read every candidate (for example the
// quota-reset selector, which clones each candidate) after Dispatch released
// the manager lock.
type cloningSelector struct{}

func (cloningSelector) Pick(_ context.Context, _ string, _ string, _ Options, auths []*Auth) (*Auth, error) {
	var picked *Auth
	for _, auth := range auths {
		if clone := auth.Clone(); clone != nil && picked == nil {
			picked = auth
		}
	}
	if picked == nil {
		return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	return picked, nil
}

func newSnapshotRegressionAuth(id string) *Auth {
	return &Auth{
		ID:       id,
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{"access_token": "token-" + id},
	}
}

// runResultMutationAgainstDispatch publishes the auth through Update, mutates
// it through MarkResult with fresh model keys, and concurrently rebuilds and
// reads scheduler shards through Dispatch.
func runResultMutationAgainstDispatch(t *testing.T, manager *Manager, base *Auth, model string) {
	t.Helper()
	const iterations = 200
	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			if _, errUpdate := manager.Update(ctx, base.Clone()); errUpdate != nil {
				t.Errorf("Update() error = %v", errUpdate)
				return
			}
			manager.MarkResult(ctx, Result{
				AuthID: base.ID,
				Model:  fmt.Sprintf("other-model-%d", i),
				Error:  &Error{Message: "upstream failure", HTTPStatus: http.StatusInternalServerError},
			})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			manager.SetConfig(nil)
			_, _ = manager.Dispatch(ctx, []string{base.Provider}, model, Options{})
		}
	}()
	wg.Wait()
}

func TestSchedulerFastPathDoesNotShareAuthWithResultMutation(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	base := newSnapshotRegressionAuth("snapshot-race-fast")
	registerDispatchTestAuth(t, manager, base, "race-model")
	runResultMutationAgainstDispatch(t, manager, base, "race-model")
}

func TestDispatchSlowPathHandsSelectorsSnapshots(t *testing.T) {
	manager := NewManager(nil, cloningSelector{}, nil)
	base := newSnapshotRegressionAuth("snapshot-race-slow")
	registerDispatchTestAuth(t, manager, base, "race-model")
	runResultMutationAgainstDispatch(t, manager, base, "race-model")
}

// dispatchedAuthIDs runs Dispatch repeatedly and returns every selected auth ID.
func dispatchedAuthIDs(t *testing.T, manager *Manager, provider, model string, rounds int) map[string]int {
	t.Helper()
	seen := make(map[string]int)
	for i := 0; i < rounds; i++ {
		decision, errDispatch := manager.Dispatch(context.Background(), []string{provider}, model, Options{})
		if errDispatch != nil {
			t.Fatalf("Dispatch() error = %v", errDispatch)
		}
		seen[decision.Auth.ID]++
	}
	return seen
}

func forEachDispatchPath(t *testing.T, run func(t *testing.T, manager *Manager)) {
	t.Helper()
	for _, path := range []struct {
		name     string
		selector Selector
	}{
		{name: "scheduler", selector: &RoundRobinSelector{}},
		{name: "selector", selector: cloningSelector{}},
	} {
		t.Run(path.name, func(t *testing.T) {
			run(t, NewManager(nil, path.selector, nil))
		})
	}
}

func TestDispatchSkipsCredentialAfterResultCooldown(t *testing.T) {
	forEachDispatchPath(t, func(t *testing.T, manager *Manager) {
		const model = "cooldown-model"
		blocked := newSnapshotRegressionAuth("cooldown-blocked-" + t.Name())
		healthy := newSnapshotRegressionAuth("cooldown-healthy-" + t.Name())
		registerDispatchTestAuth(t, manager, blocked, model)
		registerDispatchTestAuth(t, manager, healthy, model)

		manager.MarkResult(context.Background(), Result{
			AuthID: blocked.ID,
			Model:  model,
			Error:  &Error{Message: "quota", HTTPStatus: http.StatusTooManyRequests},
		})
		if seen := dispatchedAuthIDs(t, manager, blocked.Provider, model, 6); seen[blocked.ID] != 0 {
			t.Fatalf("Dispatch selected cooled-down credential: %v", seen)
		}
	})
}

func TestDispatchSkipsCredentialAfterDisable(t *testing.T) {
	forEachDispatchPath(t, func(t *testing.T, manager *Manager) {
		const model = "disable-model"
		disabled := newSnapshotRegressionAuth("disable-target-" + t.Name())
		healthy := newSnapshotRegressionAuth("disable-healthy-" + t.Name())
		registerDispatchTestAuth(t, manager, disabled, model)
		registerDispatchTestAuth(t, manager, healthy, model)

		update, ok := manager.GetByID(disabled.ID)
		if !ok {
			t.Fatal("GetByID() missing disabled target")
		}
		update.Disabled = true
		update.Status = StatusDisabled
		if _, errUpdate := manager.Update(context.Background(), update); errUpdate != nil {
			t.Fatalf("Update() error = %v", errUpdate)
		}
		if seen := dispatchedAuthIDs(t, manager, disabled.Provider, model, 6); seen[disabled.ID] != 0 {
			t.Fatalf("Dispatch selected disabled credential: %v", seen)
		}
	})
}

func TestDispatchSkipsCredentialWhileRefreshBlocksDispatch(t *testing.T) {
	forEachDispatchPath(t, func(t *testing.T, manager *Manager) {
		const model = "refresh-model"
		pending := newSnapshotRegressionAuth("refresh-target-" + t.Name())
		healthy := newSnapshotRegressionAuth("refresh-healthy-" + t.Name())
		registerDispatchTestAuth(t, manager, pending, model)
		registerDispatchTestAuth(t, manager, healthy, model)

		update, ok := manager.GetByID(pending.ID)
		if !ok {
			t.Fatal("GetByID() missing refresh target")
		}
		_ = ApplyRefreshFailureState(update, errors.New("refresh transport failure"), time.Now())
		if !RefreshBlocksDispatch(update) {
			t.Fatalf("refresh failure state does not block dispatch: %#v", update)
		}
		if _, errUpdate := manager.Update(context.Background(), update); errUpdate != nil {
			t.Fatalf("Update() error = %v", errUpdate)
		}
		if seen := dispatchedAuthIDs(t, manager, pending.Provider, model, 6); seen[pending.ID] != 0 {
			t.Fatalf("Dispatch selected refresh-blocked credential: %v", seen)
		}
	})
}

// invalidatingSelector implements InvalidateAuth so Delete and authoritative
// removal read the manager selector.
type invalidatingSelector struct {
	RoundRobinSelector
}

func (*invalidatingSelector) InvalidateAuth(string) {}

func TestSelectorReadsDoNotRaceWithSetSelector(t *testing.T) {
	manager := NewManager(nil, &invalidatingSelector{}, nil)
	const iterations = 200
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
				manager.SetSelector(&invalidatingSelector{})
			}
		}
	}()
	ctx := context.Background()
	for i := 0; i < iterations; i++ {
		auth := newSnapshotRegressionAuth(fmt.Sprintf("selector-race-%d", i))
		if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
			t.Fatalf("Register() error = %v", errRegister)
		}
		if errDelete := manager.Delete(ctx, auth.ID); errDelete != nil {
			t.Fatalf("Delete() error = %v", errDelete)
		}
		manager.removeAuthLocked(auth.ID)
		manager.stopAutoRefresh(true)
	}
	close(done)
	wg.Wait()
}

func TestDispatchFastPathPicksWithSelectorThatChoseIt(t *testing.T) {
	const model = "strategy-model"
	manager := NewManager(nil, &FillFirstSelector{}, nil)
	first := newSnapshotRegressionAuth("strategy-a")
	second := newSnapshotRegressionAuth("strategy-b")
	registerDispatchTestAuth(t, manager, first, model)
	registerDispatchTestAuth(t, manager, second, model)

	// Reproduce the window of a hot-reload swap in which the manager already
	// holds the new fill-first selector while the scheduler still applies the
	// previous round-robin strategy.
	manager.scheduler.setSelector(&RoundRobinSelector{})

	if seen := dispatchedAuthIDs(t, manager, first.Provider, model, 4); len(seen) != 1 {
		t.Fatalf("fill-first Dispatch spread picks across %v, want one credential", seen)
	}
}

func TestDeleteForgetsCooldownFence(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, nil, nil)
	for _, remove := range []struct {
		name string
		run  func(id string)
	}{
		{name: "delete", run: func(id string) {
			if errDelete := manager.Delete(ctx, id); errDelete != nil {
				t.Fatalf("Delete() error = %v", errDelete)
			}
		}},
		{name: "authoritative-removal", run: manager.removeAuthLocked},
	} {
		auth := newSnapshotRegressionAuth("fence-" + remove.name)
		if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
			t.Fatalf("Register() error = %v", errRegister)
		}
		manager.resultPersistMu.Lock()
		manager.cooldownFencePending[auth.ID] = struct{}{}
		manager.resultPersistMu.Unlock()

		remove.run(auth.ID)

		manager.resultPersistMu.Lock()
		_, pending := manager.cooldownFencePending[auth.ID]
		manager.resultPersistMu.Unlock()
		if pending {
			t.Fatalf("%s left cooldown fence for removed auth %s", remove.name, auth.ID)
		}
	}
}
