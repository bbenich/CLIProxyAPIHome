package quota

import (
	"context"
	"sort"
	"strings"
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
	c.pruneDeletedCredentials(auths)
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
		// Once the DB recorded the outcome of our attempt, its schedule wins.
		if !attempt.IsZero() && now.Before(attempt.Add(c.options.SnapshotFreshness)) && !quotaAttemptRecorded(item, attempt) {
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
		// Record the time before the claim so a DB attempt written by this
		// probe is never older than the local marker.
		attemptAt := c.options.Now().UTC()
		if c.collectCredentialWithMode(ctx, candidate.auth, false, trackIdle || candidate.manual) {
			c.attempts[candidate.auth.ID] = attemptAt
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

// quotaAttemptRecorded reports whether the DB holds a completed outcome for a
// local attempt. A claim alone leaves the row collecting without a new schedule.
func quotaAttemptRecorded(item *cluster.QuotaCredentialSnapshot, attempt time.Time) bool {
	return item != nil && item.LastAttemptAt != nil && !item.LastAttemptAt.Before(attempt) && item.CollectionStatus != "collecting"
}

// pruneDeletedCredentials drops local scheduling state for credentials that no
// longer exist. Callers must hold scheduleMu.
func (c *Collector) pruneDeletedCredentials(auths []*coreauth.Auth) {
	current := make(map[string]struct{}, len(auths))
	for _, auth := range auths {
		if auth != nil {
			current[strings.TrimSpace(auth.ID)] = struct{}{}
		}
	}
	for id := range c.attempts {
		if _, ok := current[strings.TrimSpace(id)]; !ok {
			delete(c.attempts, id)
		}
	}
	c.onDemandMu.Lock()
	for id := range c.onDemandJobs {
		if _, ok := current[id]; !ok {
			delete(c.onDemandJobs, id)
		}
	}
	c.onDemandMu.Unlock()
}
