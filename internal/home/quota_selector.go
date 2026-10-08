package home

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/config"
)

// QuotaRoutingSnapshot keeps the scheduler independent of the cluster package.
type QuotaRoutingWindow struct {
	Scope                                             string
	RemainingRatio, UsedRatio, Remaining, Used, Limit *float64
	ResetAt                                           *time.Time
	WindowSeconds                                     *int64
}
type QuotaRoutingSnapshot struct {
	ObservedAt  *time.Time
	Freshness   string
	QuotaStatus string
	Windows     []QuotaRoutingWindow
}
type quotaSnapshotReader func(context.Context, string, time.Time) (*QuotaRoutingSnapshot, error)
type quotaResetSelector struct {
	reader quotaSnapshotReader
	now    func() time.Time
}

// SetQuotaSnapshotReader wires the database view without changing CPA wire contracts.
func (r *Runtime) SetQuotaSnapshotReader(reader quotaSnapshotReader) {
	r.quotaSelector = &quotaResetSelector{reader: reader, now: time.Now}
	if r.coreManager != nil {
		r.coreManager.SetSelector(r.selectorForConfig(r.Config()))
	}
}
func (r *Runtime) selectorForConfig(cfg *config.Config) coreauth.Selector {
	if cfg != nil && strings.EqualFold(strings.TrimSpace(cfg.Routing.Strategy), "quota-reset") && r.quotaSelector != nil {
		// Session affinity would bypass dynamic quota ordering. This strategy intentionally
		// evaluates every dispatch; normal strategy settings remain unchanged.
		return r.quotaSelector
	}
	return selectorFromConfig(cfg)
}

type quotaRank struct {
	auth      *coreauth.Auth
	tier      int
	weekly    time.Time
	prime     bool
	estimated bool
}

func quotaRemaining(w QuotaRoutingWindow) (float64, bool) {
	if w.RemainingRatio != nil {
		return *w.RemainingRatio, true
	}
	if w.UsedRatio != nil {
		return 1 - *w.UsedRatio, true
	}
	if w.Limit != nil && *w.Limit > 0 {
		if w.Remaining != nil {
			return *w.Remaining / *w.Limit, true
		}
		if w.Used != nil {
			return 1 - *w.Used / *w.Limit, true
		}
	}
	return 0, false
}

func rankQuota(auth *coreauth.Auth, snapshot *QuotaRoutingSnapshot, now time.Time) quotaRank {
	rank := quotaRank{auth: auth, tier: 0}
	if snapshot == nil || snapshot.ObservedAt == nil || snapshot.ObservedAt.IsZero() || snapshot.ObservedAt.After(now) {
		return rank
	}
	// A failed probe does not invalidate a healthy last-known reset timestamp.
	// Exhaustion is evaluated on account windows below, ignoring bonus pools.
	switch snapshot.QuotaStatus {
	case "healthy", "low", "exhausted":
	default:
		return rank
	}
	fresh := snapshot.Freshness == "fresh" && now.Sub(*snapshot.ObservedAt) <= 3*time.Minute
	rank.estimated = !fresh
	var five, weekly *QuotaRoutingWindow
	for i := range snapshot.Windows {
		w := &snapshot.Windows[i]
		// Model-specific bonus pools are displayed but must not govern account-wide routing.
		if w.Scope != "account" {
			continue
		}
		if w.WindowSeconds != nil && *w.WindowSeconds == 18000 {
			five = w
		}
		if w.WindowSeconds != nil && *w.WindowSeconds == 604800 {
			weekly = w
		}
	}
	for _, w := range []*QuotaRoutingWindow{five, weekly} {
		if w == nil {
			continue
		}
		// An elapsed five-hour window must not discard a still-useful weekly timer.
		// Its old capacity/exhaustion no longer describes the current window.
		if w.ResetAt != nil && !w.ResetAt.After(now) {
			continue
		}
		if remaining, known := quotaRemaining(*w); known && remaining <= 0 {
			rank.tier = -1
			return rank
		}
	}
	if weekly != nil && weekly.ResetAt != nil && weekly.ResetAt.After(now) {
		rank.weekly = *weekly.ResetAt
		rank.tier = 1
	}
	// The provider's timer is authoritative. A successful (possibly cached)
	// request alone does not prove it started, and a running timer may still
	// report 100% after rounding. A healthy stale untouched report remains an
	// estimate only until its known weekly reset; it cannot dominate indefinitely.
	if (fresh || !rank.weekly.IsZero()) && five != nil && five.ResetAt == nil {
		if remaining, known := quotaRemaining(*five); known && remaining == 1 {
			rank.tier = 2
			rank.prime = true
		}
	}
	return rank
}

func (s *quotaResetSelector) Pick(ctx context.Context, provider, model string, opts coreauth.Options, auths []*coreauth.Auth) (*coreauth.Auth, error) {
	now := s.now()
	ranks := s.ranks(ctx, auths, now)
	candidates := make([]*coreauth.Auth, 0, len(ranks))
	for i, rank := range ranks {
		clone := rank.auth.Clone()
		if clone.Attributes == nil {
			clone.Attributes = make(map[string]string)
		}
		clone.Attributes["priority"] = strconv.Itoa(len(ranks) - i)
		candidates = append(candidates, clone)
	}
	// Reuse availability, model, cooldown, websocket, and concurrency filtering.
	chosen, err := (&coreauth.FillFirstSelector{}).Pick(ctx, provider, model, opts, candidates)
	if err != nil {
		return nil, err
	}
	for _, rank := range ranks {
		if rank.auth.ID == chosen.ID {
			return rank.auth, nil
		}
	}
	return chosen, nil
}

// ranks is shared by dispatch and observability. Neither invents timer state
// from request results; both use the same provider observations.
func (s *quotaResetSelector) ranks(ctx context.Context, auths []*coreauth.Auth, now time.Time) []quotaRank {
	ranks := make([]quotaRank, 0, len(auths))
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		var snapshot *QuotaRoutingSnapshot
		if s.reader != nil && !auth.Disabled {
			// Database reads only; provider calls never block inference selection.
			snapshot, _ = s.reader(ctx, auth.ID, now)
		}
		ranks = append(ranks, rankQuota(auth, snapshot, now))
	}
	sort.SliceStable(ranks, func(i, j int) bool {
		a, b := ranks[i], ranks[j]
		if a.tier != b.tier {
			return a.tier > b.tier
		}
		if !a.weekly.Equal(b.weekly) {
			if a.weekly.IsZero() {
				return false
			}
			if b.weekly.IsZero() {
				return true
			}
			return a.weekly.Before(b.weekly)
		}
		return a.auth.ID < b.auth.ID
	})
	return ranks
}
