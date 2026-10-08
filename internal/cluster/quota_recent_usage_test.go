package cluster

import (
	"context"
	"testing"
	"time"
)

func TestQuotaRecentUsageWindowsAliasesAndCanonicalTokens(t *testing.T) {
	ctx := context.Background()
	repo := newCredentialFoundationTestRepository(t)
	db, errDB := repo.database()
	if errDB != nil {
		t.Fatal(errDB)
	}
	// Direct fixtures model legacy records whose ID and index differ; current writes normalize them.
	auth := AuthRecord{UUID: "canonical-fixture", ID: "fixture", Index: "legacy-index", Provider: "claude", AuthJSON: JSONB(`{}`)}
	empty := AuthRecord{UUID: "canonical-unused", ID: "unused", Index: "unused-index", Provider: "codex", Disabled: true, AuthJSON: JSONB(`{}`)}
	if errCreate := db.Create(&[]AuthRecord{auth, empty}).Error; errCreate != nil {
		t.Fatal(errCreate)
	}
	now := time.Date(2026, 10, 8, 20, 0, 0, 0, time.UTC)
	add := func(at time.Time, alias, canonical string, tokens int64, current bool) {
		t.Helper()
		row := UsageRecord{Timestamp: at, AuthIndex: alias, QuotaCredentialID: canonical, Provider: "claude", TotalTokens: tokens, APIKey: "private-key", PayloadJSON: JSONB(`{}`)}
		if current {
			row.TokenAccountingVersion = UsageTokenAccountingSchemaVersion
			row.AccountingTotalTokens = tokens
			// Raw components are overlapping; current canonical totals must win.
			row.TotalTokens = 9999
			row.InputTokens = 8888
			row.CachedTokens = 7777
		}
		if errCreate := db.Create(&row).Error; errCreate != nil {
			t.Fatal(errCreate)
		}
	}
	add(now, "does-not-resolve", auth.UUID, 10, true)
	add(now.Add(-5*time.Minute), auth.UUID, "", 20, false)
	add(now.Add(-5*time.Minute-time.Nanosecond), auth.Index, "", 30, false)
	add(now.Add(-time.Hour), auth.ID, "", 40, false)
	add(now.Add(-time.Hour-time.Nanosecond), auth.Index, "", 50, false)
	add(now.Add(-24*time.Hour), auth.Index, "", 60, false)
	add(now.Add(-24*time.Hour-time.Nanosecond), auth.Index, "", 1000, false)
	add(now.Add(time.Nanosecond), auth.Index, "", 1000, false)
	add(now, "unknown", "", 1000, false)
	// A modern record with a removed UUID must not be reassigned through a reused alias.
	add(now, auth.Index, "removed-credential", 1000, false)
	// Legacy Claude totals without total_tokens include independent cache buckets.
	if errCreate := db.Create(&UsageRecord{Timestamp: now, AuthIndex: auth.Index, Provider: "claude", InputTokens: 2, OutputTokens: 3, CacheReadTokens: 4, CacheReadTokensPresent: true, CacheCreationTokens: 5, Failed: true, PayloadJSON: JSONB(`{}`)}).Error; errCreate != nil {
		t.Fatal(errCreate)
	}
	result, errRead := repo.QuotaRecentUsage(ctx, now)
	if errRead != nil {
		t.Fatal(errRead)
	}
	if !result.GeneratedAt.Equal(now) || len(result.Accounts) != 2 {
		t.Fatalf("unexpected result: %+v", result)
	}
	for _, account := range result.Accounts {
		switch account.CredentialID {
		case auth.UUID:
			if account.FiveMinutes != (QuotaUsageWindow{44, 3}) || account.Hour != (QuotaUsageWindow{114, 5}) || account.Day != (QuotaUsageWindow{224, 7}) {
				t.Fatalf("wrong rolling windows: %+v", account)
			}
		case empty.UUID:
			if account.Day != (QuotaUsageWindow{}) {
				t.Fatalf("unused account must have zero traffic: %+v", account)
			}
		default:
			t.Fatalf("unexpected credential %s", account.CredentialID)
		}
	}
}

func TestQuotaRecentUsageDuplicateAliasDoesNotMultiplyTraffic(t *testing.T) {
	ctx := context.Background()
	repo := newCredentialFoundationTestRepository(t)
	db, errDB := repo.database()
	if errDB != nil {
		t.Fatal(errDB)
	}
	for _, row := range []AuthRecord{
		{UUID: "a", ID: "id-a", Index: "shared", AuthJSON: JSONB(`{}`)},
		{UUID: "b", ID: "id-b", Index: "shared", AuthJSON: JSONB(`{}`)},
		{UUID: "shared", ID: "id-c", Index: "other", AuthJSON: JSONB(`{}`)},
	} {
		if errCreate := db.Create(&row).Error; errCreate != nil {
			t.Fatal(errCreate)
		}
	}
	now := time.Now().UTC()
	if errCreate := db.Create(&UsageRecord{Timestamp: now, AuthIndex: "shared", TotalTokens: 42, PayloadJSON: JSONB(`{}`)}).Error; errCreate != nil {
		t.Fatal(errCreate)
	}
	result, errRead := repo.QuotaRecentUsage(ctx, now)
	if errRead != nil {
		t.Fatal(errRead)
	}
	for _, row := range result.Accounts {
		want := QuotaUsageWindow{}
		if row.CredentialID == "shared" {
			want = QuotaUsageWindow{42, 1}
		}
		if row.Day != want {
			t.Fatalf("UUID precedence or deduplication failed: %+v", result)
		}
	}
	if errDelete := db.Delete(&AuthRecord{UUID: "shared"}).Error; errDelete != nil {
		t.Fatal(errDelete)
	}
	result, errRead = repo.QuotaRecentUsage(ctx, now)
	if errRead != nil {
		t.Fatal(errRead)
	}
	for _, row := range result.Accounts {
		want := QuotaUsageWindow{}
		if row.CredentialID == "a" {
			want = QuotaUsageWindow{42, 1}
		}
		if row.Day != want {
			t.Fatalf("duplicate index resolution failed: %+v", result)
		}
	}
}
