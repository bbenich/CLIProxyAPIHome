package auth

import "time"

// RoutingCandidateInfo exposes only scheduling values, never authentication data.
type RoutingCandidateInfo struct {
	Priority       int
	Weight         int64
	Blocked        string
	ModelDependent bool
}

// DescribeRoutingCandidate reuses the same parsing and availability rules as Pick.
// Without a requested model, model cooldowns are conditional, not global exclusion.
func DescribeRoutingCandidate(candidate *Auth, now time.Time) RoutingCandidateInfo {
	info := RoutingCandidateInfo{Priority: authPriority(candidate), Weight: authWeight(candidate)}
	blocked, reason, _ := isAuthBlockedForModel(candidate, "", now)
	if blocked {
		info.Blocked = "unavailable"
		if reason == blockReasonDisabled {
			info.Blocked = "disabled"
		}
		return info
	}
	for model := range candidate.ModelStates {
		if blocked, _, _ := isAuthBlockedForModel(candidate, model, now); blocked {
			info.ModelDependent = true
			break
		}
	}
	return info
}

// SchedulingSnapshot applies effective cooldown policy to detached observations.
// It does not clear or persist the underlying credential state.
func (m *Manager) SchedulingSnapshot(now time.Time) []*Auth {
	candidates := m.List()
	for i, candidate := range candidates {
		candidates[i] = m.effectiveAvailabilityAuth(candidate, now)
	}
	return candidates
}
