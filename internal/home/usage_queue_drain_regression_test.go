package home

import (
	"context"
	"sync"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// drainTestUsageStore is a controllable cluster adapter and usage store.
type drainTestUsageStore struct {
	started chan struct{}
	release chan struct{}

	mu     sync.Mutex
	stored []string
	once   sync.Once
}

func (s *drainTestUsageStore) Enabled() bool                       { return true }
func (s *drainTestUsageStore) LoadAuthIndex(context.Context) error { return nil }
func (s *drainTestUsageStore) ListMinimalAuths() []*coreauth.Auth  { return nil }
func (s *drainTestUsageStore) LoadConfigYAML(context.Context) ([]byte, error) {
	return nil, nil
}
func (s *drainTestUsageStore) GetFullAuth(context.Context, string) (*coreauth.Auth, error) {
	return nil, nil
}

// StoreUsagePayload blocks the first write until release closes or ctx ends,
// and fails like a database call when ctx is already canceled.
func (s *drainTestUsageStore) StoreUsagePayload(ctx context.Context, payload string, _ time.Time) error {
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
	case <-ctx.Done():
	}
	if errCtx := ctx.Err(); errCtx != nil {
		return errCtx
	}
	s.mu.Lock()
	s.stored = append(s.stored, payload)
	s.mu.Unlock()
	return nil
}

func (s *drainTestUsageStore) storedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.stored)
}

func startDrainTestRuntime(t *testing.T, store *drainTestUsageStore) *Runtime {
	t.Helper()
	runCtx, cancel := context.WithCancel(context.Background())
	r := &Runtime{clusterAdapter: store, cancel: cancel}
	r.startClusterUsageWriter(runCtx)
	return r
}

func waitForStop(t *testing.T, r *Runtime) {
	t.Helper()
	stopped := make(chan struct{})
	go func() {
		r.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("runtime stop did not return")
	}
}

func TestClusterUsageWriterDrainsAcceptedPayloadsOnStop(t *testing.T) {
	store := &drainTestUsageStore{started: make(chan struct{}), release: make(chan struct{})}
	r := startDrainTestRuntime(t, store)
	for index := 0; index < 10; index++ {
		accepted, errPersist := r.PersistClusterUsagePayload(context.Background(), `{"provider":"codex"}`, time.Time{})
		if errPersist != nil || !accepted {
			t.Fatalf("PersistClusterUsagePayload() = %v, %v", accepted, errPersist)
		}
	}
	<-store.started
	close(store.release)
	waitForStop(t, r)
	if got := store.storedCount(); got != 10 {
		t.Fatalf("stored = %d, want 10 accepted payloads", got)
	}
}

func TestClusterUsageWriterDrainDeadlineIsBounded(t *testing.T) {
	previousTimeout := clusterUsageDrainTimeout
	clusterUsageDrainTimeout = 20 * time.Millisecond
	t.Cleanup(func() { clusterUsageDrainTimeout = previousTimeout })
	hook := logtest.NewGlobal()
	t.Cleanup(func() { log.StandardLogger().ReplaceHooks(make(log.LevelHooks)) })

	// The store never releases, so only the drain deadline can end shutdown.
	store := &drainTestUsageStore{started: make(chan struct{}), release: make(chan struct{})}
	r := startDrainTestRuntime(t, store)
	for index := 0; index < 3; index++ {
		if accepted, errPersist := r.PersistClusterUsagePayload(context.Background(), `{"provider":"codex"}`, time.Time{}); errPersist != nil || !accepted {
			t.Fatalf("PersistClusterUsagePayload() = %v, %v", accepted, errPersist)
		}
	}
	<-store.started
	waitForStop(t, r)
	if got := store.storedCount(); got != 0 {
		t.Fatalf("stored = %d, want 0", got)
	}
	dropped := -1
	for _, entry := range hook.AllEntries() {
		if value, ok := entry.Data["dropped"].(int); ok {
			dropped = value
		}
	}
	if dropped != 3 {
		t.Fatalf("logged dropped = %d, want 3", dropped)
	}
}
