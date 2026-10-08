package home

import (
	"context"
	"sort"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
)

type RoutingAccountObservation struct {
	CredentialID   string     `json:"credential_id"`
	Rank           *int       `json:"rank"`
	Reason         string     `json:"reason"`
	Priority       int        `json:"priority"`
	Weight         int64      `json:"weight"`
	WeeklyResetAt  *time.Time `json:"weekly_reset_at,omitempty"`
	ModelDependent bool       `json:"model_dependent"`
	Estimated      bool       `json:"estimated,omitempty"`
}
type RoutingObservation struct {
	Strategy        string                      `json:"strategy"`
	StrategyName    string                      `json:"strategy_name"`
	OrderKind       string                      `json:"order_kind"`
	SessionAffinity bool                        `json:"session_affinity"`
	GeneratedAt     time.Time                   `json:"generated_at"`
	Accounts        []RoutingAccountObservation `json:"accounts"`
}

func routingStrategyName(strategy string) (string, string, string) {
	switch strings.ToLower(strings.TrimSpace(strategy)) {
	case "quota-reset":
		return "quota-reset", "Quota reset priority", "rank"
	case "fill-first", "fillfirst", "ff":
		return "fill-first", "Fill first", "rank"
	case "weighted-round-robin", "weightedroundrobin", "wrr":
		return "weighted-round-robin", "Weighted round robin", "tier"
	case "round-robin", "roundrobin", "rr", "":
		return "round-robin", "Round robin", "tier"
	default:
		return strategy, strategy, "unknown"
	}
}

// RoutingObservation reports global ranking values, before request-specific group,
// provider/model, session-affinity, retry and concurrency restrictions. Rotating
// selectors expose tied priority tiers, not a fictional next-request queue.
func (r *Runtime) RoutingObservation(ctx context.Context) RoutingObservation {
	cfg := r.Config()
	strategy := "round-robin"
	affinity := false
	if cfg != nil {
		strategy = cfg.Routing.Strategy
		affinity = cfg.Routing.SessionAffinity
	}
	id, name, kind := routingStrategyName(strategy)
	now := time.Now()
	out := RoutingObservation{Strategy: id, StrategyName: name, OrderKind: kind, SessionAffinity: affinity && id != "quota-reset", GeneratedAt: now, Accounts: []RoutingAccountObservation{}}
	if r == nil || r.coreManager == nil {
		return out
	}
	auths := r.coreManager.SchedulingSnapshot(now)
	if id == "quota-reset" && r.quotaSelector == nil {
		out.OrderKind = "unknown"
		for _, auth := range auths {
			out.Accounts = append(out.Accounts, RoutingAccountObservation{CredentialID: auth.ID, Reason: "routing-unavailable"})
		}
		return out
	}
	if id == "quota-reset" && r.quotaSelector != nil {
		s := r.quotaSelector
		now = s.now()
		out.GeneratedAt = now
		ranks := s.ranks(ctx, auths, now)
		for _, rank := range ranks {
			info := coreauth.DescribeRoutingCandidate(rank.auth, now)
			item := RoutingAccountObservation{CredentialID: rank.auth.ID, Priority: info.Priority, Weight: info.Weight, ModelDependent: info.ModelDependent, Estimated: rank.estimated}
			switch rank.tier {
			case 2:
				item.Reason = "untouched-five-hour"
			case 1:
				item.Reason = "weekly-reset"
			case -1:
				item.Reason = "quota-exhausted"
			default:
				item.Reason = "quota-unknown"
			}
			if !rank.weekly.IsZero() {
				reset := rank.weekly
				item.WeeklyResetAt = &reset
			}
			if info.Blocked != "" {
				item.Reason = info.Blocked
			} else {
				position := 1
				for _, existing := range out.Accounts {
					if existing.Rank != nil {
						position++
					}
				}
				item.Rank = &position
			}
			out.Accounts = append(out.Accounts, item)
		}
		return out
	}
	sort.Slice(auths, func(i, j int) bool {
		a, b := coreauth.DescribeRoutingCandidate(auths[i], now), coreauth.DescribeRoutingCandidate(auths[j], now)
		if a.Priority != b.Priority {
			return a.Priority > b.Priority
		}
		return auths[i].ID < auths[j].ID
	})
	lastPriority, position := 0, 0
	for _, auth := range auths {
		info := coreauth.DescribeRoutingCandidate(auth, now)
		item := RoutingAccountObservation{CredentialID: auth.ID, Priority: info.Priority, Weight: info.Weight, ModelDependent: info.ModelDependent, Reason: "static-priority"}
		if id == "weighted-round-robin" {
			item.Reason = "weighted-rotation"
		} else if id == "round-robin" {
			item.Reason = "rotation"
		}
		if info.Blocked != "" {
			item.Reason = info.Blocked
		} else if id == "weighted-round-robin" && info.Weight <= 0 {
			item.Reason = "zero-weight"
		} else if kind == "unknown" {
			item.Reason = "unsupported-strategy"
		} else {
			if kind == "rank" || position == 0 || lastPriority != info.Priority {
				position++
			}
			rank := position
			item.Rank = &rank
			lastPriority = info.Priority
		}
		out.Accounts = append(out.Accounts, item)
	}
	return out
}
