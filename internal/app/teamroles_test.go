package app_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/simonhauck/keycloak-outline-group-sync/internal/app"
)

func TestRunOnceFailsFastWhenTeamRoleFallbackInvalid(t *testing.T) {
	for _, value := range []string{"boss", "Admin", "owner"} {
		t.Run(value, func(t *testing.T) {
			validEnv(t)
			t.Setenv("TEAM_ROLE_FALLBACK", value)

			err := app.RunOnce(context.Background())
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !strings.Contains(err.Error(), "TEAM_ROLE_FALLBACK") {
				t.Fatalf("error %q does not name the invalid setting TEAM_ROLE_FALLBACK", err)
			}
		})
	}
}

func TestRunOnceFailsFastWhenRoleSignalConfiguredTwice(t *testing.T) {
	validEnv(t)
	t.Setenv("TEAM_ROLE_ADMIN", "Admins")
	t.Setenv("TEAM_ROLE_VIEWER", "Admins, Viewers")

	err := app.RunOnce(context.Background())
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "Admins") {
		t.Fatalf("error %q does not name the duplicated Role Signal", err)
	}
	if !strings.Contains(err.Error(), "TEAM_ROLE_ADMIN") || !strings.Contains(err.Error(), "TEAM_ROLE_VIEWER") {
		t.Fatalf("error %q does not name both settings", err)
	}
}

func TestRunOnceFailsBeforeOutlineWhenRoleSignalIsUnknown(t *testing.T) {
	kc := newFakeKeycloak(t, []clientRole{{ID: "role-a-uuid", Name: "Team A"}})
	ol := newFakeOutline(t)
	syncEnv(t, kc, ol)
	t.Setenv("TEAM_ROLE_ADMIN", "Not-A-Role")

	err := app.RunOnce(context.Background())
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "Not-A-Role") {
		t.Fatalf("error %q does not name the unknown Role Signal", err)
	}
	if requests := ol.allRequests(); len(requests) != 0 {
		t.Fatalf("an unknown Role Signal must abort before touching Outline, got: %+v", requests)
	}
}

func TestRunOnceDoesNotCreateManagedGroupForRoleSignal(t *testing.T) {
	kc := newFakeKeycloak(t, []clientRole{
		{ID: "role-signal-uuid", Name: "outline-admin"},
		{ID: "role-group-uuid", Name: "Team A"},
	})
	ol := newFakeOutline(t)
	syncEnv(t, kc, ol)
	t.Setenv("TEAM_ROLE_ADMIN", "outline-admin")

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	assertGroups(t, ol, []outlineGroup{{
		ID:         "created-group-1",
		Name:       "Team A",
		ExternalID: "keycloak:test:roles-client:role-group-uuid",
	}})
}

func TestRunOnceLeavesManagedGroupOfFormerRoleUntouchedWhenItBecomesARoleSignal(t *testing.T) {
	kc := newFakeKeycloak(t, []clientRole{{ID: "role-signal-uuid", Name: "outline-admin"}})
	ol := newFakeOutline(t)
	ol.seedGroups(outlineGroup{
		ID:         "former-group",
		Name:       "outline-admin",
		ExternalID: "keycloak:test:roles-client:role-signal-uuid",
	})
	ol.seedUsers(outlineUser{ID: "outline-alice", Email: "alice@example.com"})
	ol.seedMembership("former-group", "outline-alice")
	syncEnv(t, kc, ol)
	t.Setenv("TEAM_ROLE_ADMIN", "outline-admin")

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	assertGroups(t, ol, []outlineGroup{{
		ID:         "former-group",
		Name:       "outline-admin",
		ExternalID: "keycloak:test:roles-client:role-signal-uuid",
	}})
	assertMembers(t, ol, "former-group", []string{"outline-alice"})
	if writes := ol.writeRequests(); len(writes) != 0 {
		t.Fatalf("a Managed Group whose role became a Role Signal must not be written to, got: %+v", writes)
	}
}

func TestRunOnceAppliesFallbackTeamRoleToManagedAccounts(t *testing.T) {
	kc := newFakeKeycloak(t, nil)
	kc.seedUsers(keycloakUser{
		ID: "kc-alice", Username: "alice", Email: "alice@example.com", Enabled: true,
	})
	ol := newFakeOutline(t)
	ol.seedUsers(
		outlineUser{ID: "outline-alice", Email: "alice@example.com", Role: "member"},
		outlineUser{ID: "outline-bystander", Email: "bystander@example.com", Role: "admin"},
	)
	syncEnv(t, kc, ol)

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if got := ol.userRole("outline-alice"); got != "viewer" {
		t.Fatalf("alice's Team Role = %q, want the fallback viewer", got)
	}
	if got := ol.userRole("outline-bystander"); got != "admin" {
		t.Fatalf("an Outline Account without a Keycloak user was touched: role = %q, want admin", got)
	}
	if writes := ol.writeRequests(); len(writes) != 1 || writes[0].Path != "/api/users.update_role" {
		t.Fatalf("expected exactly one users.update_role write, got: %+v", writes)
	}

	writesAfterFirst := len(ol.writeRequests())
	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("second RunOnce: %v", err)
	}
	if writesAfterSecond := len(ol.writeRequests()); writesAfterSecond != writesAfterFirst {
		t.Fatalf("second Sync Run performed %d extra write(s); Team Roles are already in sync",
			writesAfterSecond-writesAfterFirst)
	}
}

func TestRunOnceAppliesTheHighestTeamRoleSignal(t *testing.T) {
	kc := newFakeKeycloak(t, []clientRole{
		{ID: "role-admin-uuid", Name: "outline-admin"},
		{ID: "role-member-uuid", Name: "outline-member"},
		{ID: "role-viewer-uuid", Name: "outline-viewer"},
		{ID: "role-guest-uuid", Name: "outline-guest"},
	})
	kc.seedUsers(
		keycloakUser{
			ID: "kc-alice", Username: "alice", Email: "alice@example.com", Enabled: true,
			DirectRoles: []string{"outline-admin", "outline-guest"},
		},
		keycloakUser{
			ID: "kc-bob", Username: "bob", Email: "bob@example.com", Enabled: true,
			DirectRoles: []string{"outline-viewer", "outline-member"},
		},
		keycloakUser{
			ID: "kc-carol", Username: "carol", Email: "carol@example.com", Enabled: true,
			DirectRoles: []string{"outline-viewer", "outline-guest"},
		},
	)
	ol := newFakeOutline(t)
	ol.seedUsers(
		outlineUser{ID: "outline-alice", Email: "alice@example.com", Role: "guest"},
		outlineUser{ID: "outline-bob", Email: "bob@example.com", Role: "admin"},
		outlineUser{ID: "outline-carol", Email: "carol@example.com", Role: "member"},
	)
	syncEnv(t, kc, ol)
	t.Setenv("TEAM_ROLE_ADMIN", "outline-admin")
	t.Setenv("TEAM_ROLE_MEMBER", "outline-member")
	t.Setenv("TEAM_ROLE_VIEWER", "outline-viewer")
	t.Setenv("TEAM_ROLE_GUEST", "outline-guest")

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	for id, want := range map[string]string{
		"outline-alice": "admin",
		"outline-bob":   "member",
		"outline-carol": "viewer",
	} {
		if got := ol.userRole(id); got != want {
			t.Fatalf("Team Role of %s = %q, want %q", id, got, want)
		}
	}
}

func TestRunOnceComputesTeamRoleFromInheritedRoleSignal(t *testing.T) {
	kc := newFakeKeycloak(t, []clientRole{{ID: "role-admin-uuid", Name: "outline-admin"}})
	kc.seedGroups(keycloakGroup{
		ID: "g-admins", Roles: []string{"outline-admin"}, Members: []string{"kc-alice"},
	})
	kc.seedUsers(keycloakUser{
		ID: "kc-alice", Username: "alice", Email: "alice@example.com", Enabled: true,
	})
	ol := newFakeOutline(t)
	ol.seedUsers(outlineUser{ID: "outline-alice", Email: "alice@example.com", Role: "viewer"})
	syncEnv(t, kc, ol)
	t.Setenv("TEAM_ROLE_ADMIN", "outline-admin")

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if got := ol.userRole("outline-alice"); got != "admin" {
		t.Fatalf("Team Role of alice = %q, want admin through the Keycloak Group", got)
	}
}

func TestRunOnceUsesConfiguredFallbackTeamRole(t *testing.T) {
	kc := newFakeKeycloak(t, []clientRole{{ID: "role-guest-uuid", Name: "outline-guest"}})
	kc.seedUsers(
		keycloakUser{
			ID: "kc-alice", Username: "alice", Email: "alice@example.com", Enabled: true,
		},
		keycloakUser{
			ID: "kc-dave", Username: "dave", Email: "dave@example.com", Enabled: true,
			DirectRoles: []string{"outline-guest"},
		},
	)
	ol := newFakeOutline(t)
	ol.seedUsers(
		outlineUser{ID: "outline-alice", Email: "alice@example.com", Role: "viewer"},
		outlineUser{ID: "outline-dave", Email: "dave@example.com", Role: "member"},
	)
	syncEnv(t, kc, ol)
	t.Setenv("TEAM_ROLE_GUEST", "outline-guest")
	t.Setenv("TEAM_ROLE_FALLBACK", "member")

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if got := ol.userRole("outline-alice"); got != "member" {
		t.Fatalf("alice's Team Role = %q, want the configured fallback member", got)
	}
	if got := ol.userRole("outline-dave"); got != "guest" {
		t.Fatalf("dave's Team Role = %q; a Role Signal must win over the Fallback Team Role", got)
	}
}

func TestRunOnceAppliesFallbackToDisabledKeycloakUsers(t *testing.T) {
	kc := newFakeKeycloak(t, []clientRole{{ID: "role-admin-uuid", Name: "outline-admin"}})
	kc.seedUsers(keycloakUser{
		ID: "kc-carol", Username: "carol", Email: "carol@example.com", Enabled: false,
		DirectRoles: []string{"outline-admin"},
	})
	ol := newFakeOutline(t)
	ol.seedUsers(outlineUser{ID: "outline-carol", Email: "carol@example.com", Role: "admin"})
	syncEnv(t, kc, ol)
	t.Setenv("TEAM_ROLE_ADMIN", "outline-admin")

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if got := ol.userRole("outline-carol"); got != "viewer" {
		t.Fatalf("a disabled Keycloak user kept Team Role %q, want the fallback viewer", got)
	}
}

func TestRunOnceAcceptsFallbackTeamRoleInAnyCase(t *testing.T) {
	kc := newFakeKeycloak(t, nil)
	kc.seedUsers(keycloakUser{
		ID: "kc-alice", Username: "alice", Email: "alice@example.com", Enabled: true,
	})
	ol := newFakeOutline(t)
	ol.seedUsers(outlineUser{ID: "outline-alice", Email: "alice@example.com", Role: "member"})
	syncEnv(t, kc, ol)
	t.Setenv("TEAM_ROLE_FALLBACK", "VIEWER")

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if got := ol.userRole("outline-alice"); got != "viewer" {
		t.Fatalf("Team Role of alice = %q, want the viewer fallback", got)
	}
}

func TestRunOnceLeavesAmbiguousAccountsTeamRoleUntouched(t *testing.T) {
	kc := newFakeKeycloak(t, []clientRole{{ID: "role-admin-uuid", Name: "outline-admin"}})
	kc.seedUsers(
		keycloakUser{
			ID: "kc-eve-one", Username: "eve-one", Email: "eve@example.com", Enabled: true,
			DirectRoles: []string{"outline-admin"},
		},
		keycloakUser{
			ID: "kc-eve-two", Username: "eve-two", Email: "EVE@example.com", Enabled: true,
			DirectRoles: []string{"outline-admin"},
		},
	)
	ol := newFakeOutline(t)
	ol.seedUsers(outlineUser{ID: "outline-eve", Email: "eve@example.com", Role: "member"})
	syncEnv(t, kc, ol)
	t.Setenv("TEAM_ROLE_ADMIN", "outline-admin")

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if got := ol.userRole("outline-eve"); got != "member" {
		t.Fatalf("an ambiguously matched Outline Account changed Team Role to %q, want member", got)
	}
	if writes := ol.writeRequests(); len(writes) != 0 {
		t.Fatalf("expected no writes for an ambiguous account, got: %+v", writes)
	}
}

func TestRunOnceSkipsTheTokenOwnerButSyncsOthers(t *testing.T) {
	kc := newFakeKeycloak(t, nil)
	kc.seedUsers(
		keycloakUser{ID: "kc-owner", Username: "owner", Email: "owner@example.com", Enabled: true},
		keycloakUser{ID: "kc-alice", Username: "alice", Email: "alice@example.com", Enabled: true},
	)
	ol := newFakeOutline(t)
	ol.seedTokenOwner(outlineUser{ID: "outline-owner", Email: "owner@example.com", Role: "admin"})
	ol.seedUsers(
		outlineUser{ID: "outline-owner", Email: "owner@example.com", Role: "admin"},
		outlineUser{ID: "outline-alice", Email: "alice@example.com", Role: "member"},
	)
	syncEnv(t, kc, ol)

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if got := ol.userRole("outline-owner"); got != "admin" {
		t.Fatalf("the token owner's Team Role changed to %q; Outline forbids self-role-change", got)
	}
	if got := ol.userRole("outline-alice"); got != "viewer" {
		t.Fatalf("alice's Team Role = %q; a skipped token owner must not stop other writes", got)
	}
	if writes := ol.writeRequests(); len(writes) != 1 || writes[0].JSON["id"] != "outline-alice" {
		t.Fatalf("expected exactly the alice role write, got: %+v", writes)
	}
}

func TestRunOnceDryRunPlansTeamRolesWithoutWriting(t *testing.T) {
	kc := newFakeKeycloak(t, []clientRole{{ID: "role-admin-uuid", Name: "outline-admin"}})
	kc.seedUsers(keycloakUser{
		ID: "kc-alice", Username: "alice", Email: "alice@example.com", Enabled: true,
		DirectRoles: []string{"outline-admin"},
	})
	ol := newFakeOutline(t)
	ol.seedUsers(outlineUser{ID: "outline-alice", Email: "alice@example.com", Role: "viewer"})
	syncEnv(t, kc, ol)
	t.Setenv("TEAM_ROLE_ADMIN", "outline-admin")
	t.Setenv("DRY_RUN", "true")

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if got := ol.userRole("outline-alice"); got != "viewer" {
		t.Fatalf("dry run changed a Team Role to %q", got)
	}
	if writes := ol.writeRequests(); len(writes) != 0 {
		t.Fatalf("dry run must not write to Outline, got: %+v", writes)
	}
}

func TestRunOnceReportsFailedTeamRoleWriteAndContinues(t *testing.T) {
	kc := newFakeKeycloak(t, nil)
	kc.seedUsers(
		keycloakUser{ID: "kc-alice", Username: "alice", Email: "alice@example.com", Enabled: true},
		keycloakUser{ID: "kc-bob", Username: "bob", Email: "bob@example.com", Enabled: true},
	)
	ol := newFakeOutline(t)
	ol.seedUsers(
		outlineUser{ID: "outline-alice", Email: "alice@example.com", Role: "member"},
		outlineUser{ID: "outline-bob", Email: "bob@example.com", Role: "member"},
	)
	ol.failNextUpdateRole(1)
	syncEnv(t, kc, ol)

	err := app.RunOnce(context.Background())
	if err == nil {
		t.Fatal("expected the Sync Run to report the failed Team Role write")
	}

	roles := []string{ol.userRole("outline-alice"), ol.userRole("outline-bob")}
	slices.Sort(roles)
	if !slices.Equal(roles, []string{"member", "viewer"}) {
		t.Fatalf("failed role write did not leave the other account synced: %v", roles)
	}
}
