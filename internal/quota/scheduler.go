package quota

import (
	"context"
	"sort"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPIHome/internal/cluster"
)

func scheduledSnapshotDue(item *cluster.QuotaCredentialSnapshot, now time.Time) bool {
	if item == nil {
		return false
	}
	if item.NextProbeAt != nil {
		return !item.NextProbeAt.After(now)
	}
	return item.ExpiresAt == nil || !item.ExpiresAt.After(now)
}

// One account per slot, ordered by the latest attempted check rather than the
// latest successful snapshot. Failures rotate to the back and keep their DB
// backoff. The claim rechecks eligibility, leases and scheduling atomically.
func (c *Collector) collectStaggered(ctx context.Context, auths []*coreauth.Auth) {
	c.scheduleMu.Lock()
	defer c.scheduleMu.Unlock()
	now := c.options.Now().UTC()
	if !c.lastSlot.IsZero() && now.Before(c.lastSlot.Add(c.options.StaggerInterval)) {
		return
	}
	trackIdle := c.options.TrackIdleCredentials != nil && c.options.TrackIdleCredentials()
	type candidate struct {
		auth    *coreauth.Auth
		attempt time.Time
		manual  bool
	}
	candidates := make([]candidate, 0, len(auths))
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		c.onDemandMu.Lock()
		_, manual := c.onDemandJobs[auth.ID]
		c.onDemandMu.Unlock()
		if !quotaProbeEligible(auth) {
			if manual {
				c.releaseOnDemandJob(auth)
			}
			continue
		}
		item, err := c.repo.GetQuotaCredential(ctx, auth.ID, now)
		if err != nil {
			continue
		}
		if !scheduledSnapshotDue(item, now) {
			if manual {
				c.releaseOnDemandJob(auth)
			}
			continue
		}
		attempt := c.attempts[auth.ID]
		// A persistence failure must not let the same credential win every slot.
		if !attempt.IsZero() && now.Before(attempt.Add(c.options.SnapshotFreshness)) {
			continue
		}
		if item.LastAttemptAt != nil && item.LastAttemptAt.After(attempt) {
			attempt = *item.LastAttemptAt
		}
		candidates = append(candidates, candidate{auth, attempt, manual})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].attempt.Equal(candidates[j].attempt) {
			return candidates[i].auth.ID < candidates[j].auth.ID
		}
		return candidates[i].attempt.Before(candidates[j].attempt)
	})
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			return
		}
		if c.collectCredentialWithMode(ctx, candidate.auth, false, trackIdle || candidate.manual) {
			c.attempts[candidate.auth.ID] = c.options.Now().UTC()
			c.lastSlot = c.options.Now().UTC()
			if candidate.manual {
				c.releaseOnDemandJob(candidate.auth)
			}
			return
		}
		// Lease contention or a concurrent credential change doesn't consume the
		// slot: try the next account, but never perform two claimed probes.
		if candidate.manual {
			c.releaseOnDemandJob(candidate.auth)
		}
	}
}
