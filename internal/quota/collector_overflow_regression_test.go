package quota

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
)

func TestQuotaRetryAfterClampsHugeHints(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	for _, value := range []string{"Fri, 31 Dec 9999 23:59:59 GMT", "9000000000", "9223372036854775807"} {
		got := quotaRetryAfter(http.Header{"Retry-After": []string{value}}, now)
		if got != maxFailureBackoff {
			t.Fatalf("quotaRetryAfter(%q) = %v, want %v", value, got, maxFailureBackoff)
		}
	}
	if got := quotaRetryAfter(http.Header{"Retry-After": []string{"600"}}, now); got != 10*time.Minute {
		t.Fatalf("quotaRetryAfter(600) = %v, want 10m", got)
	}
}

func TestQuotaBackoffWithJitterDoesNotOverflow(t *testing.T) {
	for _, id := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		delay := time.Duration(math.MaxInt64 - 1)
		if got := quotaBackoffWithJitter(delay, id); got < delay {
			t.Fatalf("quotaBackoffWithJitter(%q) = %v overflowed below %v", id, got, delay)
		}
	}
}

func TestQuotaFailureScheduleFarFutureRetryAfterStaysBounded(t *testing.T) {
	for _, retryAfter := range []string{"Fri, 31 Dec 9999 23:59:59 GMT", "9000000000"} {
		t.Run(retryAfter, func(t *testing.T) {
			repo := newCollectorTestRepository(t)
			now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
			seedCollectorProviderAuth(t, repo, "rate-limited", "codex", map[string]any{"type": "codex", "access_token": "fixture-token"})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", retryAfter)
				w.WriteHeader(http.StatusTooManyRequests)
			}))
			defer server.Close()
			c := NewCollector(repo, Options{Owner: "fixture", CodexUsageURL: server.URL, Now: func() time.Time { return now }, TrackIdleCredentials: func() bool { return true }})
			c.collect(context.Background())
			item, errGet := repo.GetQuotaCredential(context.Background(), "rate-limited", now)
			if errGet != nil || item.NextProbeAt == nil {
				t.Fatalf("GetQuotaCredential() = %+v, %v", item, errGet)
			}
			upper := now.Add(maxFailureBackoff + maxFailureBackoff/5)
			if !item.NextProbeAt.After(now) || item.NextProbeAt.After(upper) {
				t.Fatalf("next_probe_at = %v, want within (%v, %v]", item.NextProbeAt, now, upper)
			}
		})
	}
}

func TestParseCodexUsageRejectsOverflowingResetAfterSeconds(t *testing.T) {
	observedAt := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	body := []byte(`{"rate_limit":{"primary_window":{"used_percent":20,"limit_window_seconds":604800,"reset_after_seconds":10000000000}}}`)
	usage, errParse := parseCodexUsage(body, observedAt)
	if errParse != nil || len(usage.windows) != 1 {
		t.Fatalf("parseCodexUsage() = %+v, %v", usage, errParse)
	}
	if resetAt := usage.windows[0].ResetAt; resetAt != nil {
		t.Fatalf("reset_at = %v, want nil for an out-of-range hint", resetAt)
	}

	body = []byte(`{"rate_limit":{"primary_window":{"used_percent":20,"limit_window_seconds":604800,"reset_after_seconds":3600}}}`)
	usage, errParse = parseCodexUsage(body, observedAt)
	if errParse != nil || len(usage.windows) != 1 || usage.windows[0].ResetAt == nil || !usage.windows[0].ResetAt.Equal(observedAt.Add(time.Hour)) {
		t.Fatalf("in-range reset_after_seconds not applied: %+v, %v", usage, errParse)
	}
}

func TestParseKimiUsageRejectsOverflowingResetIn(t *testing.T) {
	observedAt := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	for _, resetIn := range []string{"1e10", "1e300"} {
		body := []byte(`{"usage":{"limit":"100","used":"10","resetIn":` + resetIn + `},"limits":[{"name":"weekly","limit":"100","used":"10","resetIn":` + resetIn + `}]}`)
		windows, errParse := parseKimiUsageWindows(body, observedAt)
		if errParse != nil || len(windows) != 2 {
			t.Fatalf("parseKimiUsageWindows(%s) = %d windows, %v", resetIn, len(windows), errParse)
		}
		for _, window := range windows {
			if window.ResetAt != nil {
				t.Fatalf("resetIn=%s window %s reset_at = %v, want nil", resetIn, window.ID, window.ResetAt)
			}
		}
	}
	body := []byte(`{"limits":[{"name":"weekly","limit":"100","used":"10","resetIn":3600}]}`)
	windows, errParse := parseKimiUsageWindows(body, observedAt)
	if errParse != nil || len(windows) != 1 || windows[0].ResetAt == nil || !windows[0].ResetAt.Equal(observedAt.Add(time.Hour)) {
		t.Fatalf("in-range resetIn not applied: %+v, %v", windows, errParse)
	}
}

// The local attempt gate must not override a shorter DB failure backoff once
// the DB recorded the attempt outcome.
func TestQuotaStaggeredTrustsRecordedDBBackoff(t *testing.T) {
	repo := newCollectorTestRepository(t)
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	seedCollectorProviderAuth(t, repo, "flaky", "codex", map[string]any{"type": "codex", "access_token": "fixture-token"})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	c := NewCollector(repo, Options{Owner: "paced", CodexUsageURL: server.URL, Now: func() time.Time { return now }, SnapshotFreshness: 30 * time.Minute, StaggerInterval: 15 * time.Second, TrackIdleCredentials: func() bool { return true }})
	c.collect(context.Background())
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
	item, errGet := repo.GetQuotaCredential(context.Background(), "flaky", now)
	if errGet != nil || item.NextProbeAt == nil || item.NextProbeAt.After(now.Add(7*time.Minute)) {
		t.Fatalf("unexpected failure schedule %+v %v", item, errGet)
	}
	now = now.Add(7 * time.Minute)
	c.collect(context.Background())
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2: local gate overrode the DB backoff", calls.Load())
	}
}

// Without a DB record of the attempt, the local gate still blocks repeat wins.
func TestQuotaStaggeredLocalGateAppliesWithoutDBRecord(t *testing.T) {
	repo := newCollectorTestRepository(t)
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	seedCollectorProviderAuth(t, repo, "unrecorded", "codex", map[string]any{"type": "codex", "access_token": "fixture-token"})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	c := NewCollector(repo, Options{Owner: "paced", CodexUsageURL: server.URL, Now: func() time.Time { return now }, SnapshotFreshness: time.Minute, StaggerInterval: 15 * time.Second, TrackIdleCredentials: func() bool { return true }})
	c.attempts["unrecorded"] = now
	c.collect(context.Background())
	if calls.Load() != 0 {
		t.Fatal("local gate ignored an attempt the DB did not record")
	}
	now = now.Add(time.Minute)
	c.collect(context.Background())
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1 after the local gate expired", calls.Load())
	}
}

func TestQuotaStaggeredPrunesDeletedCredentialState(t *testing.T) {
	repo := newCollectorTestRepository(t)
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	c := NewCollector(repo, Options{Owner: "paced", Now: func() time.Time { return now }, StaggerInterval: 15 * time.Second})
	c.attempts["deleted"] = now
	c.attempts["kept"] = now
	c.onDemandJobs["deleted"] = struct{}{}
	c.onDemandJobs["kept"] = struct{}{}
	c.lastSlot = now // Prune even when the slot is not due yet.
	c.collectStaggered(context.Background(), []*coreauth.Auth{{ID: "kept", Provider: "codex"}})
	if _, ok := c.attempts["deleted"]; ok {
		t.Fatal("attempts retained a deleted credential")
	}
	if _, ok := c.onDemandJobs["deleted"]; ok {
		t.Fatal("onDemandJobs retained a deleted credential")
	}
	if _, ok := c.attempts["kept"]; !ok {
		t.Fatal("attempts dropped a live credential")
	}
	if _, ok := c.onDemandJobs["kept"]; !ok {
		t.Fatal("onDemandJobs dropped a live credential")
	}
}
