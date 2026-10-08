package cluster

import (
	"context"
	coreauth "github.com/router-for-me/CLIProxyAPIHome/internal/cliproxy/auth"
	"reflect"
	"testing"
)

func TestQuotaUserScopesUnionAndModelIntersections(t *testing.T) {
	ctx := context.Background()
	repo := newCredentialFoundationTestRepository(t)
	for _, id := range []string{"a", "b", "c"} {
		if _, err := repo.UpsertAuth(ctx, &coreauth.Auth{ID: id, Index: id, Provider: "claude", Disabled: id == "c"}, "test"); err != nil {
			t.Fatal(err)
		}
	}
	group := func(name string, disabled bool, ids ...string) uint {
		g, err := repo.CreateChannelGroup(ctx, name, disabled)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range ids {
			if _, err := repo.CreateChannelGroupDetail(ctx, g.ID, id); err != nil {
				t.Fatal(err)
			}
		}
		return g.ID
	}
	a := group("A", false, "a", "b")
	b := group("B", false, "b", "c")
	disabled := group("disabled", true, "c")
	modelGroup, err := repo.CreateModelGroup(ctx, "model-subset", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateModelGroupDetail(ctx, modelGroup.ID, "model", []uint{b}); err != nil {
		t.Fatal(err)
	}
	emptyModel, err := repo.CreateModelGroup(ctx, "empty", false)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		channels [][]uint
		models   []uint
		want     []string
	}{
		{"no keys", nil, nil, []string{}},
		{"credential scope", [][]uint{{a}}, nil, []string{"a", "b"}},
		{"union across keys", [][]uint{{a}, {b}}, nil, []string{"a", "b", "c"}},
		{"unscoped key", [][]uint{{}}, nil, []string{"a", "b", "c"}},
		{"disabled scope", [][]uint{{disabled}}, nil, []string{}},
		{"model scope intersection", [][]uint{{a}}, []uint{modelGroup.ID}, []string{"b"}},
		{"model scope only", [][]uint{{}}, []uint{modelGroup.ID}, []string{"b", "c"}},
		{"empty model scope", [][]uint{{a}}, []uint{emptyModel.ID}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name := tc.name
			u, err := repo.CreateUser(ctx, UserUpdate{Username: &name})
			if err != nil {
				t.Fatal(err)
			}
			for i, ch := range tc.channels {
				key := name + string(rune('0'+i))
				if _, err := repo.CreateAPIKey(ctx, APIKeyEntryUpdate{APIKey: key, UserID: &u.ID, UserIDSet: true, Channels: &ch, ModelGroups: &tc.models}); err != nil {
					t.Fatal(err)
				}
			}
			scopes, err := repo.QuotaUserScopes(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, scope := range scopes {
				if scope.ID == u.ID {
					if !reflect.DeepEqual(scope.CredentialIDs, tc.want) || scope.KeyCount != len(tc.channels) {
						t.Fatalf("got %+v, want IDs %v", scope, tc.want)
					}
					return
				}
			}
			t.Fatal("user missing")
		})
	}
	// Deleting a key removes its access on the next read, and unbound keys do not grant user access.
	name := "deleted-key"
	u, err := repo.CreateUser(ctx, UserUpdate{Username: &name})
	if err != nil {
		t.Fatal(err)
	}
	key, err := repo.CreateAPIKey(ctx, APIKeyEntryUpdate{APIKey: "deleted-fixture", UserID: &u.ID, UserIDSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteAPIKey(ctx, APIKeySelector{ID: key.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateAPIKey(ctx, APIKeyEntryUpdate{APIKey: "unbound-fixture"}); err != nil {
		t.Fatal(err)
	}
	scopes, err := repo.QuotaUserScopes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range scopes {
		if scope.ID == u.ID && (len(scope.CredentialIDs) != 0 || scope.KeyCount != 0) {
			t.Fatalf("deleted/unbound key granted access: %+v", scope)
		}
	}
}
