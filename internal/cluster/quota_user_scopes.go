package cluster

import (
	"context"
	"sort"
)

// QuotaUserScope is configured credential access across a user's active keys.
// It contains no key values or authentication material. Temporary billing,
// cooldown and account-disabled states are shown separately by the dashboard.
type QuotaUserScope struct {
	ID            uint     `json:"id"`
	Username      string   `json:"username"`
	KeyCount      int      `json:"key_count"`
	CredentialIDs []string `json:"credential_ids"`
}

func (r *Repository) QuotaUserScopes(ctx context.Context) ([]QuotaUserScope, error) {
	db, errDB := r.database()
	if errDB != nil {
		return nil, errDB
	}
	users, errUsers := r.ListUsers(ctx)
	if errUsers != nil {
		return nil, errUsers
	}
	var allIDs []string
	if errIDs := db.WithContext(contextOrBackground(ctx)).Model(&AuthRecord{}).Order("uuid").Pluck("uuid", &allIDs).Error; errIDs != nil {
		return nil, errIDs
	}
	existing := make(map[string]struct{}, len(allIDs))
	for _, id := range allIDs {
		existing[id] = struct{}{}
	}
	result := make([]QuotaUserScope, 0, len(users))
	for _, user := range users {
		keys, errKeys := r.ListAPIKeyRecordsForUser(ctx, user.ID)
		if errKeys != nil {
			return nil, errKeys
		}
		ids := make(map[string]struct{})
		for i := range keys {
			// The same scope helpers used by dispatch exclude disabled/deleted groups.
			base, errBase := allowedAuthIDsForAPIKeyRecord(ctx, db, &keys[i])
			if errBase != nil {
				return nil, errBase
			}
			details, restricted, errDetails := allowedModelGroupDetailsForAPIKeyRecord(ctx, db, &keys[i])
			if errDetails != nil {
				return nil, errDetails
			}
			var models []string
			if restricted {
				models = allowedModelIDsFromDetails(details, true)
			} else {
				models = []string{""}
			}
			for _, model := range models {
				allowed := base
				channels, modelRestricted, errChannels := modelChannelGroupIDsFromDetails(details, model)
				if errChannels != nil {
					return nil, errChannels
				}
				if modelRestricted {
					modelIDs, errModelIDs := allowedAuthIDsForChannelGroups(ctx, db, channels)
					if errModelIDs != nil {
						return nil, errModelIDs
					}
					allowed = intersectAllowedAuthIDs(base, modelIDs)
				}
				if allowed == nil {
					allowed = allIDs
				}
				for _, id := range allowed {
					if _, exists := existing[id]; exists {
						ids[id] = struct{}{}
					}
				}
			}
		}
		scope := QuotaUserScope{ID: user.ID, Username: user.Username, KeyCount: len(keys), CredentialIDs: make([]string, 0, len(ids))}
		for id := range ids {
			scope.CredentialIDs = append(scope.CredentialIDs, id)
		}
		sort.Strings(scope.CredentialIDs)
		result = append(result, scope)
	}
	return result, nil
}
