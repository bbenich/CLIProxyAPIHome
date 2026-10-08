package cluster

import (
	"context"
	"fmt"
	"time"
)

type QuotaUsageWindow struct {
	Tokens   int64 `json:"tokens"`
	Requests int64 `json:"requests"`
}

type QuotaRecentUsage struct {
	CredentialID string           `json:"credential_id"`
	FiveMinutes  QuotaUsageWindow `json:"five_minutes"`
	Hour         QuotaUsageWindow `json:"hour"`
	Day          QuotaUsageWindow `json:"day"`
}

type QuotaRecentUsageResult struct {
	GeneratedAt time.Time          `json:"generated_at"`
	Accounts    []QuotaRecentUsage `json:"accounts"`
}

// QuotaRecentUsage summarizes completed proxy traffic, not provider quota consumption.
// Modern records retain their ingestion-time UUID. Legacy aliases resolve with
// the same UUID/index/ID precedence as ingestion, without joins multiplying rows.
func (r *Repository) QuotaRecentUsage(ctx context.Context, now time.Time) (QuotaRecentUsageResult, error) {
	now = now.UTC()
	result := QuotaRecentUsageResult{GeneratedAt: now, Accounts: []QuotaRecentUsage{}}
	db, errDB := r.database()
	if errDB != nil {
		return result, errDB
	}
	var credentials []AuthRecord
	if errFind := db.WithContext(contextOrBackground(ctx)).Select("uuid", "index", "id").Order("uuid ASC").Find(&credentials).Error; errFind != nil {
		return result, errFind
	}
	positions := make(map[string]int, len(credentials))
	aliases := make(map[string]string, len(credentials)*3)
	for _, credential := range credentials {
		positions[credential.UUID] = len(result.Accounts)
		result.Accounts = append(result.Accounts, QuotaRecentUsage{CredentialID: credential.UUID})
	}
	// Sorted UUID order resolves duplicate aliases deterministically, just as ingestion does.
	for _, field := range []func(AuthRecord) string{
		func(a AuthRecord) string { return a.UUID },
		func(a AuthRecord) string { return a.Index },
		func(a AuthRecord) string { return a.ID },
	} {
		for _, credential := range credentials {
			alias := field(credential)
			if _, exists := aliases[alias]; alias != "" && !exists {
				aliases[alias] = credential.UUID
			}
		}
	}
	var rows []struct {
		CredentialID string `gorm:"column:credential_id"`
		AuthIndex    string
		FiveTokens   int64
		FiveRequests int64
		HourTokens   int64
		HourRequests int64
		DayTokens    int64
		DayRequests  int64
	}
	tokens := usageObservabilitySQLAccountingTotalTokens(`"usage"`)
	selection := fmt.Sprintf(`quota_credential_id AS credential_id, auth_index,
		SUM(CASE WHEN timestamp >= ? THEN %s ELSE 0 END) AS five_tokens,
		SUM(CASE WHEN timestamp >= ? THEN 1 ELSE 0 END) AS five_requests,
		SUM(CASE WHEN timestamp >= ? THEN %s ELSE 0 END) AS hour_tokens,
		SUM(CASE WHEN timestamp >= ? THEN 1 ELSE 0 END) AS hour_requests,
		SUM(%s) AS day_tokens, COUNT(*) AS day_requests`, tokens, tokens, tokens)
	if errRead := db.WithContext(contextOrBackground(ctx)).Table("usage").
		Where("timestamp >= ? AND timestamp <= ?", now.Add(-24*time.Hour), now).
		Select(selection, now.Add(-5*time.Minute), now.Add(-5*time.Minute), now.Add(-time.Hour), now.Add(-time.Hour)).
		Group("quota_credential_id, auth_index").Scan(&rows).Error; errRead != nil {
		return result, errRead
	}
	for _, row := range rows {
		id := row.CredentialID
		if id == "" {
			id = aliases[row.AuthIndex]
		}
		position, exists := positions[id]
		if !exists {
			continue
		}
		account := &result.Accounts[position]
		account.FiveMinutes.Tokens += row.FiveTokens
		account.FiveMinutes.Requests += row.FiveRequests
		account.Hour.Tokens += row.HourTokens
		account.Hour.Requests += row.HourRequests
		account.Day.Tokens += row.DayTokens
		account.Day.Requests += row.DayRequests
	}
	return result, nil
}
