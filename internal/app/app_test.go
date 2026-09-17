package app_test

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/simonhauck/keycloak-outline-group-sync/internal/app"
)

func validEnv(t *testing.T) {
	t.Helper()
	t.Setenv("KEYCLOAK_URL", "http://keycloak.invalid")
	t.Setenv("KEYCLOAK_REALM", testRealm)
	t.Setenv("KEYCLOAK_CLIENT_ID", testClientID)
	t.Setenv("KEYCLOAK_CLIENT_SECRET", testClientSecret)
	t.Setenv("OUTLINE_URL", "http://outline.invalid")
	t.Setenv("OUTLINE_TOKEN", testOutlineToken)
}

func syncEnv(t *testing.T, kc *fakeKeycloak, ol *fakeOutline) {
	t.Helper()
	t.Setenv("KEYCLOAK_URL", kc.URL())
	t.Setenv("KEYCLOAK_REALM", testRealm)
	t.Setenv("KEYCLOAK_CLIENT_ID", testClientID)
	t.Setenv("KEYCLOAK_CLIENT_SECRET", testClientSecret)
	t.Setenv("KEYCLOAK_ROLES_CLIENT_ID", testRolesClient)
	t.Setenv("OUTLINE_URL", ol.URL())
	t.Setenv("OUTLINE_TOKEN", testOutlineToken)
	t.Setenv("LOG_LEVEL", "error")
}

func assertGroups(t *testing.T, ol *fakeOutline, want []outlineGroup) {
	t.Helper()
	if got := ol.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Outline groups:\n got: %+v\nwant: %+v", got, want)
	}
}

func seedDryRunFixture(t *testing.T) (*fakeKeycloak, *fakeOutline) {
	t.Helper()
	kc := newFakeKeycloak(t, []clientRole{
		{ID: "role-a-uuid", Name: "Team A"},
		{ID: "role-b-uuid", Name: "Team B"},
		{ID: "role-c-uuid", Name: "Team C"},
	})
	kc.seedUsers(
		keycloakUser{
			ID: "kc-alice", Username: "alice", Email: "alice@example.com", Enabled: true,
			DirectRoles: []string{"Team A"},
		},
		keycloakUser{
			ID: "kc-dave", Username: "dave", Email: "dave@example.com", Enabled: true,
			DirectRoles: []string{"Team B"},
		},
		keycloakUser{
			ID: "kc-erin", Username: "erin", Email: "erin@example.com", Enabled: true,
			DirectRoles: []string{"Team C"},
		},
	)
	ol := newFakeOutline(t)
	ol.seedGroups(
		outlineGroup{ID: "managed-a", Name: "Team Old", ExternalID: "keycloak:test:roles-client:role-a-uuid"},
		outlineGroup{ID: "adoptable-c", Name: "team c"},
	)
	ol.seedUsers(
		outlineUser{ID: "outline-alice", Email: "alice@example.com"},
		outlineUser{ID: "outline-bob", Email: "bob@example.com"},
		outlineUser{ID: "outline-dave", Email: "dave@example.com"},
		outlineUser{ID: "outline-erin", Email: "erin@example.com"},
	)
	ol.seedMembership("managed-a", "outline-alice", "outline-bob")
	return kc, ol
}

func TestRunOnceFailsFastWhenRequiredSettingMissing(t *testing.T) {
	for _, name := range []string{
		"KEYCLOAK_URL",
		"KEYCLOAK_REALM",
		"KEYCLOAK_CLIENT_ID",
		"KEYCLOAK_CLIENT_SECRET",
		"OUTLINE_URL",
		"OUTLINE_TOKEN",
	} {
		t.Run(name, func(t *testing.T) {
			validEnv(t)
			t.Setenv(name, "")

			err := app.RunOnce(context.Background())
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !strings.Contains(err.Error(), name) {
				t.Fatalf("error %q does not name the missing setting %s", err, name)
			}
		})
	}
}

func TestRunOnceFailsFastWhenSettingInvalid(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
	}{
		{"KEYCLOAK_URL", "keycloak.invalid"},
		{"KEYCLOAK_URL", "ftp://keycloak.invalid"},
		{"OUTLINE_URL", "://"},
		{"LOG_LEVEL", "loud"},
		{"SYNC_INTERVAL", "not-a-duration"},
		{"SYNC_INTERVAL", "-1s"},
		{"SYNC_INTERVAL", "0s"},
		{"DRY_RUN", "sometimes"},
	} {
		t.Run(tc.name+"="+tc.value, func(t *testing.T) {
			validEnv(t)
			t.Setenv(tc.name, tc.value)

			err := app.RunOnce(context.Background())
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !strings.Contains(err.Error(), tc.name) {
				t.Fatalf("error %q does not name the invalid setting %s", err, tc.name)
			}
		})
	}
}

func TestRunOnceWithNoClientRolesWritesNothing(t *testing.T) {
	kc := newFakeKeycloak(t, nil)
	ol := newFakeOutline(t)
	syncEnv(t, kc, ol)

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if writes := ol.writeRequests(); len(writes) != 0 {
		t.Fatalf("expected no Outline writes, got %d: %+v", len(writes), writes)
	}
	if len(kc.allRequests()) == 0 {
		t.Fatal("expected the Sync Run to read from Keycloak")
	}
	var listedGroups bool
	for _, request := range ol.allRequests() {
		if request.Path == "/api/groups.list" {
			listedGroups = true
		}
	}
	if !listedGroups {
		t.Fatal("expected the Sync Run to list Outline Groups")
	}
}

func TestRunOnceCreatesManagedGroupPerClientRole(t *testing.T) {
	kc := newFakeKeycloak(t, []clientRole{
		{ID: "role-a-uuid", Name: "Team A"},
		{ID: "role-b-uuid", Name: "Team B"},
	})
	ol := newFakeOutline(t)
	syncEnv(t, kc, ol)

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	assertGroups(t, ol, []outlineGroup{
		{ID: "created-group-1", Name: "Team A", ExternalID: "keycloak:test:roles-client:role-a-uuid"},
		{ID: "created-group-2", Name: "Team B", ExternalID: "keycloak:test:roles-client:role-b-uuid"},
	})
}

func TestRunOnceAdoptsCaseInsensitiveNameMatch(t *testing.T) {
	kc := newFakeKeycloak(t, []clientRole{{ID: "role-uuid", Name: "Team A"}})
	ol := newFakeOutline(t)
	ol.seedGroups(outlineGroup{ID: "existing-group", Name: "team a"})
	syncEnv(t, kc, ol)

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	assertGroups(t, ol, []outlineGroup{{
		ID:         "existing-group",
		Name:       "Team A",
		ExternalID: "keycloak:test:roles-client:role-uuid",
	}})
}

func TestRunOnceRenamesManagedGroupWhenClientRoleRenamed(t *testing.T) {
	kc := newFakeKeycloak(t, []clientRole{{ID: "role-uuid", Name: "Team Renamed"}})
	ol := newFakeOutline(t)
	ol.seedGroups(outlineGroup{
		ID:         "managed-group",
		Name:       "Team Old",
		ExternalID: "keycloak:test:roles-client:role-uuid",
	})
	syncEnv(t, kc, ol)

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	assertGroups(t, ol, []outlineGroup{{
		ID:         "managed-group",
		Name:       "Team Renamed",
		ExternalID: "keycloak:test:roles-client:role-uuid",
	}})
}

func TestRunOnceLeavesUnmanagedGroupsUntouched(t *testing.T) {
	kc := newFakeKeycloak(t, []clientRole{{ID: "role-uuid", Name: "Team A"}})
	ol := newFakeOutline(t)
	ol.seedGroups(
		outlineGroup{ID: "manual-group", Name: "Handbook"},
		outlineGroup{ID: "foreign-group", Name: "Team A", ExternalID: "other-sync:42"},
	)
	syncEnv(t, kc, ol)

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	assertGroups(t, ol, []outlineGroup{
		{ID: "manual-group", Name: "Handbook"},
		{ID: "foreign-group", Name: "Team A", ExternalID: "other-sync:42"},
		{ID: "created-group-1", Name: "Team A", ExternalID: "keycloak:test:roles-client:role-uuid"},
	})
	if writes := ol.writeRequests(); len(writes) != 1 || writes[0].Path != "/api/groups.create" {
		t.Fatalf("expected only the create write, got: %+v", writes)
	}
}

func TestRunOncePaginatesClientRolesAndGroups(t *testing.T) {
	var roles []clientRole
	var groups []outlineGroup
	var want []outlineGroup
	for i := 1; i <= 5; i++ {
		name := fmt.Sprintf("Team %d", i)
		roles = append(roles, clientRole{ID: fmt.Sprintf("role-%d", i), Name: name})
		groups = append(groups, outlineGroup{ID: fmt.Sprintf("group-%d", i), Name: strings.ToLower(name)})
		want = append(want, outlineGroup{
			ID:         fmt.Sprintf("group-%d", i),
			Name:       name,
			ExternalID: fmt.Sprintf("keycloak:test:roles-client:role-%d", i),
		})
	}
	kc := newFakeKeycloak(t, roles)
	kc.pageSize = 2
	ol := newFakeOutline(t)
	ol.pageSize = 2
	ol.seedGroups(groups...)
	syncEnv(t, kc, ol)

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	assertGroups(t, ol, want)

	var listCalls int
	for _, request := range ol.allRequests() {
		if request.Path == "/api/groups.list" {
			listCalls++
		}
	}
	if listCalls < 3 {
		t.Fatalf("expected Outline group listing to be paginated, got %d list call(s)", listCalls)
	}
}

func TestRunOnceTwiceWithUnchangedStatePerformsNoWrites(t *testing.T) {
	kc := newFakeKeycloak(t, []clientRole{{ID: "role-uuid", Name: "Team A"}})
	ol := newFakeOutline(t)
	syncEnv(t, kc, ol)

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("first Sync Run: %v", err)
	}
	afterFirst := ol.snapshot()
	writesAfterFirst := len(ol.writeRequests())
	if writesAfterFirst == 0 {
		t.Fatal("first Sync Run should have created the Managed Group")
	}

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("second Sync Run: %v", err)
	}
	if writesAfterSecond := len(ol.writeRequests()); writesAfterSecond != writesAfterFirst {
		t.Fatalf("second Sync Run performed %d write(s), want none beyond the first Sync Run's %d",
			writesAfterSecond-writesAfterFirst, writesAfterFirst)
	}
	if afterSecond := ol.snapshot(); !reflect.DeepEqual(afterFirst, afterSecond) {
		t.Fatalf("second Sync Run changed Outline groups:\nbefore: %+v\n after: %+v", afterFirst, afterSecond)
	}
}

func TestRunOnceRolesClientDefaultsToAuthClient(t *testing.T) {
	kc := newFakeKeycloak(t, []clientRole{{ID: "role-uuid", Name: "Team A"}})
	kc.clients = []keycloakClientRep{{ID: testRolesUUID, ClientID: testClientID}}
	ol := newFakeOutline(t)
	syncEnv(t, kc, ol)
	t.Setenv("KEYCLOAK_ROLES_CLIENT_ID", "")

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	assertGroups(t, ol, []outlineGroup{{
		ID:         "created-group-1",
		Name:       "Team A",
		ExternalID: "keycloak:test:sync-client:role-uuid",
	}})
}

func TestRunOnceAuthenticatesWithClientCredentials(t *testing.T) {
	kc := newFakeKeycloak(t, nil)
	ol := newFakeOutline(t)
	syncEnv(t, kc, ol)

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var tokenRequest *recordedRequest
	for _, request := range kc.allRequests() {
		if strings.HasSuffix(request.Path, "/protocol/openid-connect/token") {
			tokenRequest = &request
		}
		if request.Form.Has("password") {
			t.Fatalf("request %s carried a password; the service must use client credentials only", request.Path)
		}
	}
	if tokenRequest == nil {
		t.Fatal("expected a token request")
	}
	if got := tokenRequest.Form.Get("grant_type"); got != "client_credentials" {
		t.Fatalf("grant_type = %q, want client_credentials", got)
	}
	if got := tokenRequest.Form.Get("client_id"); got != testClientID {
		t.Fatalf("client_id = %q, want %q", got, testClientID)
	}
	if got := tokenRequest.Form.Get("client_secret"); got != testClientSecret {
		t.Fatalf("client_secret = %q, want %q", got, testClientSecret)
	}
}

func TestRunOnceFailsWhenRolesClientUnknown(t *testing.T) {
	kc := newFakeKeycloak(t, nil)
	ol := newFakeOutline(t)
	syncEnv(t, kc, ol)
	t.Setenv("KEYCLOAK_ROLES_CLIENT_ID", "does-not-exist")

	err := app.RunOnce(context.Background())
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Fatalf("error %q does not name the unknown roles client", err)
	}
}

func runServiceUntil(t *testing.T, run func(context.Context) error, observed func() int, want int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan error, 1)
	go func() { runDone <- run(ctx) }()

	deadline := time.After(5 * time.Second)
	for observed() < want {
		select {
		case err := <-runDone:
			t.Fatalf("Run returned after %d of %d expected runs: %v", observed(), want, err)
		case <-deadline:
			t.Fatalf("timed out after observing %d of %d runs", observed(), want)
		case <-time.After(time.Millisecond):
		}
	}

	cancel()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
	if got := observed(); got < want {
		t.Fatalf("observed %d runs, want at least %d", got, want)
	}
}

func TestRunPerformsSyncRunsOnInterval(t *testing.T) {
	kc := newFakeKeycloak(t, nil)
	ol := newFakeOutline(t)
	syncEnv(t, kc, ol)
	t.Setenv("SYNC_INTERVAL", "10ms")

	runServiceUntil(t, app.Run, ol.listCalls, 3)
}

func TestRunContinuesAfterFailedSyncRun(t *testing.T) {
	kc := newFakeKeycloak(t, []clientRole{{ID: "role-uuid", Name: "Team A"}})
	ol := newFakeOutline(t)
	ol.failNextListGroups(1)
	syncEnv(t, kc, ol)
	t.Setenv("SYNC_INTERVAL", "10ms")

	runServiceUntil(t, app.Run, func() int { return len(ol.snapshot()) }, 1)
	assertMembers(t, ol, "created-group-1", nil)
	if calls := ol.listCalls(); calls < 2 {
		t.Fatalf("expected a retry after the failed Sync Run, got %d Outline group listing(s)", calls)
	}
}

func TestRunDoesNotOverlapSyncRuns(t *testing.T) {
	kc := newFakeKeycloak(t, nil)
	ol := newFakeOutline(t)
	ol.listGroupsDelay = 100 * time.Millisecond
	syncEnv(t, kc, ol)
	t.Setenv("SYNC_INTERVAL", "10ms")

	runServiceUntil(t, app.Run, ol.listCalls, 2)

	if got := ol.maxListInFlight(); got != 1 {
		t.Fatalf("max concurrent groups.list calls = %d, want 1 (Sync Runs must not overlap)", got)
	}
}

func TestRunPerformsSyncRunAtStartup(t *testing.T) {
	kc := newFakeKeycloak(t, nil)
	ol := newFakeOutline(t)
	syncEnv(t, kc, ol)
	t.Setenv("SYNC_INTERVAL", "1h")

	runServiceUntil(t, app.Run, ol.listCalls, 1)
}

func assertMembers(t *testing.T, ol *fakeOutline, groupID string, want []string) {
	t.Helper()
	got := ol.groupMembers(groupID)
	slices.Sort(got)
	want = append([]string(nil), want...)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("members of Outline Group %s:\n got: %v\nwant: %v", groupID, got, want)
	}
}

func TestRunOnceAddsUserWithDirectClientRole(t *testing.T) {
	kc := newFakeKeycloak(t, []clientRole{{ID: "role-uuid", Name: "Team A"}})
	kc.seedUsers(keycloakUser{
		ID:          "kc-alice",
		Username:    "alice",
		Email:       "alice@example.com",
		Enabled:     true,
		DirectRoles: []string{"Team A"},
	})
	ol := newFakeOutline(t)
	ol.seedUsers(outlineUser{ID: "outline-alice", Email: "alice@example.com"})
	syncEnv(t, kc, ol)

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	assertMembers(t, ol, "created-group-1", []string{"outline-alice"})
}

func TestRunOnceAddsUserInheritingClientRoleThroughKeycloakGroups(t *testing.T) {
	kc := newFakeKeycloak(t, []clientRole{{ID: "role-uuid", Name: "Team A"}})
	kc.seedGroups(
		keycloakGroup{ID: "parent-group", Roles: []string{"Team A"}},
		keycloakGroup{ID: "child-group", ParentID: "parent-group", Members: []string{"kc-bob"}},
	)
	kc.seedUsers(keycloakUser{ID: "kc-bob", Username: "bob", Email: "bob@example.com", Enabled: true})
	ol := newFakeOutline(t)
	ol.seedUsers(outlineUser{ID: "outline-bob", Email: "bob@example.com"})
	syncEnv(t, kc, ol)

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	assertMembers(t, ol, "created-group-1", []string{"outline-bob"})
}

func TestRunOnceSkipsDisabledUsers(t *testing.T) {
	kc := newFakeKeycloak(t, []clientRole{{ID: "role-uuid", Name: "Team A"}})
	kc.seedUsers(keycloakUser{
		ID: "kc-carol", Username: "carol", Email: "carol@example.com", Enabled: false,
		DirectRoles: []string{"Team A"},
	})
	ol := newFakeOutline(t)
	ol.seedUsers(outlineUser{ID: "outline-carol", Email: "carol@example.com"})
	syncEnv(t, kc, ol)

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	assertMembers(t, ol, "created-group-1", nil)
	if writes := ol.writeRequests(); len(writes) != 1 || writes[0].Path != "/api/groups.create" {
		t.Fatalf("expected only the group create write, got: %+v", writes)
	}
}

func TestRunOnceSkipsUsersWithoutEmail(t *testing.T) {
	kc := newFakeKeycloak(t, []clientRole{{ID: "role-uuid", Name: "Team A"}})
	kc.seedUsers(keycloakUser{
		ID: "kc-dave", Username: "dave", Email: "", Enabled: true,
		DirectRoles: []string{"Team A"},
	})
	ol := newFakeOutline(t)
	syncEnv(t, kc, ol)

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	assertMembers(t, ol, "created-group-1", nil)
	for _, request := range ol.allRequests() {
		if request.Path != "/api/users.list" {
			continue
		}
		for _, email := range request.JSON["emails"].([]any) {
			if email == "" {
				t.Fatalf("users.list was queried with an empty email: %+v", request.JSON)
			}
		}
	}
}

func TestRunOnceSkipsUsersSharingAnEmailAddress(t *testing.T) {
	kc := newFakeKeycloak(t, []clientRole{{ID: "role-uuid", Name: "Team A"}})
	kc.seedUsers(
		keycloakUser{
			ID: "kc-eve-one", Username: "eve-one", Email: "eve@example.com", Enabled: true,
			DirectRoles: []string{"Team A"},
		},
		keycloakUser{
			ID: "kc-eve-two", Username: "eve-two", Email: "EVE@example.com", Enabled: true,
			DirectRoles: []string{"Team A"},
		},
	)
	ol := newFakeOutline(t)
	ol.seedUsers(outlineUser{ID: "outline-eve", Email: "eve@example.com"})
	syncEnv(t, kc, ol)

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	assertMembers(t, ol, "created-group-1", nil)
	if writes := ol.writeRequests(); len(writes) != 1 || writes[0].Path != "/api/groups.create" {
		t.Fatalf("expected only the group create write, got: %+v", writes)
	}
}

func TestRunOnceSkipsUsersWithoutOutlineAccount(t *testing.T) {
	kc := newFakeKeycloak(t, []clientRole{{ID: "role-uuid", Name: "Team A"}})
	kc.seedUsers(keycloakUser{
		ID: "kc-frank", Username: "frank", Email: "frank@example.com", Enabled: true,
		DirectRoles: []string{"Team A"},
	})
	ol := newFakeOutline(t)
	syncEnv(t, kc, ol)

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	assertMembers(t, ol, "created-group-1", nil)
	if writes := ol.writeRequests(); len(writes) != 1 || writes[0].Path != "/api/groups.create" {
		t.Fatalf("expected only the group create write, got: %+v", writes)
	}
	for _, request := range ol.allRequests() {
		if strings.Contains(request.Path, "invite") || strings.Contains(request.Path, "users.create") {
			t.Fatalf("the service must not create accounts, saw request: %+v", request)
		}
	}
}

func TestRunOnceMatchesOutlineAccountEmailCaseInsensitively(t *testing.T) {
	kc := newFakeKeycloak(t, []clientRole{{ID: "role-uuid", Name: "Team A"}})
	kc.seedUsers(keycloakUser{
		ID: "kc-grace", Username: "grace", Email: "Grace@Example.COM", Enabled: true,
		DirectRoles: []string{"Team A"},
	})
	ol := newFakeOutline(t)
	ol.seedUsers(outlineUser{ID: "outline-grace", Email: "grace@example.com"})
	syncEnv(t, kc, ol)

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	assertMembers(t, ol, "created-group-1", []string{"outline-grace"})
}

func TestRunOncePaginatesUsersAccountsAndMemberships(t *testing.T) {
	kc := newFakeKeycloak(t, []clientRole{{ID: "role-uuid", Name: "Team A"}})
	var users []keycloakUser
	var accounts []outlineUser
	var want []string
	for i := 1; i <= 5; i++ {
		username := fmt.Sprintf("user-%d", i)
		email := username + "@example.com"
		users = append(users, keycloakUser{
			ID: "kc-" + username, Username: username, Email: email, Enabled: true,
			DirectRoles: []string{"Team A"},
		})
		accounts = append(accounts, outlineUser{ID: "outline-" + username, Email: email})
		want = append(want, "outline-"+username)
	}
	kc.seedUsers(users...)
	kc.pageSize = 2
	ol := newFakeOutline(t)
	ol.pageSize = 2
	ol.seedUsers(accounts...)
	syncEnv(t, kc, ol)

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	assertMembers(t, ol, "created-group-1", want)
}

func TestRunOnceTwiceKeepsMembershipWithoutAdditionalWrites(t *testing.T) {
	kc := newFakeKeycloak(t, []clientRole{{ID: "role-uuid", Name: "Team A"}})
	kc.seedUsers(keycloakUser{
		ID: "kc-alice", Username: "alice", Email: "alice@example.com", Enabled: true,
		DirectRoles: []string{"Team A"},
	})
	ol := newFakeOutline(t)
	ol.seedUsers(outlineUser{ID: "outline-alice", Email: "alice@example.com"})
	syncEnv(t, kc, ol)

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("first RunOnce: %v", err)
	}
	assertMembers(t, ol, "created-group-1", []string{"outline-alice"})
	writesAfterFirst := len(ol.writeRequests())

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("second RunOnce: %v", err)
	}
	if writesAfterSecond := len(ol.writeRequests()); writesAfterSecond != writesAfterFirst {
		t.Fatalf("second Sync Run performed %d extra write(s); members are already in sync",
			writesAfterSecond-writesAfterFirst)
	}
	assertMembers(t, ol, "created-group-1", []string{"outline-alice"})
}

func TestRunOnceCreatesManagedGroupWithNoRoleHolders(t *testing.T) {
	kc := newFakeKeycloak(t, []clientRole{{ID: "role-uuid", Name: "Team A"}})
	ol := newFakeOutline(t)
	syncEnv(t, kc, ol)

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	assertGroups(t, ol, []outlineGroup{{
		ID:         "created-group-1",
		Name:       "Team A",
		ExternalID: "keycloak:test:roles-client:role-uuid",
	}})
	assertMembers(t, ol, "created-group-1", nil)
	if writes := ol.writeRequests(); len(writes) != 1 || writes[0].Path != "/api/groups.create" {
		t.Fatalf("expected only the group create write, got: %+v", writes)
	}
}

func TestRunOnceRemovesMemberWhoLostClientRole(t *testing.T) {
	kc := newFakeKeycloak(t, []clientRole{{ID: "role-uuid", Name: "Team A"}})
	ol := newFakeOutline(t)
	ol.seedGroups(outlineGroup{
		ID: "managed-group", Name: "Team A", ExternalID: "keycloak:test:roles-client:role-uuid",
	})
	ol.seedUsers(outlineUser{ID: "outline-alice", Email: "alice@example.com"})
	ol.seedMembership("managed-group", "outline-alice")
	syncEnv(t, kc, ol)

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	assertMembers(t, ol, "managed-group", nil)
	if writes := ol.writeRequests(); len(writes) != 1 || writes[0].Path != "/api/groups.remove_user" {
		t.Fatalf("expected only one remove_user write, got: %+v", writes)
	}
}

func TestRunOnceRemovesDisabledUserFromManagedGroup(t *testing.T) {
	kc := newFakeKeycloak(t, []clientRole{{ID: "role-uuid", Name: "Team A"}})
	kc.seedUsers(keycloakUser{
		ID: "kc-carol", Username: "carol", Email: "carol@example.com", Enabled: false,
		DirectRoles: []string{"Team A"},
	})
	ol := newFakeOutline(t)
	ol.seedGroups(outlineGroup{
		ID: "managed-group", Name: "Team A", ExternalID: "keycloak:test:roles-client:role-uuid",
	})
	ol.seedUsers(outlineUser{ID: "outline-carol", Email: "carol@example.com"})
	ol.seedMembership("managed-group", "outline-carol")
	syncEnv(t, kc, ol)

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	assertMembers(t, ol, "managed-group", nil)
}

func TestRunOnceRemovesExtraMembersOnlyFromManagedGroups(t *testing.T) {
	kc := newFakeKeycloak(t, []clientRole{{ID: "role-uuid", Name: "Team A"}})
	ol := newFakeOutline(t)
	ol.seedGroups(
		outlineGroup{ID: "managed-group", Name: "Team A", ExternalID: "keycloak:test:roles-client:role-uuid"},
		outlineGroup{ID: "unmanaged-group", Name: "Handbook"},
	)
	ol.seedUsers(
		outlineUser{ID: "outline-alice", Email: "alice@example.com"},
		outlineUser{ID: "outline-bob", Email: "bob@example.com"},
	)
	ol.seedMembership("managed-group", "outline-alice")
	ol.seedMembership("unmanaged-group", "outline-bob")
	syncEnv(t, kc, ol)

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	assertMembers(t, ol, "managed-group", nil)
	assertMembers(t, ol, "unmanaged-group", []string{"outline-bob"})
	for _, write := range ol.writeRequests() {
		if write.JSON["id"] == "unmanaged-group" {
			t.Fatalf("the service wrote to an unmanaged Outline Group: %+v", write)
		}
	}
}

func TestRunOnceLeavesOrphanedManagedGroupsUntouched(t *testing.T) {
	kc := newFakeKeycloak(t, []clientRole{{ID: "role-uuid", Name: "Team A"}})
	ol := newFakeOutline(t)
	ol.seedGroups(outlineGroup{
		ID: "orphaned-group", Name: "Old Team", ExternalID: "keycloak:test:roles-client:deleted-role-uuid",
	})
	ol.seedUsers(outlineUser{ID: "outline-alice", Email: "alice@example.com"})
	ol.seedMembership("orphaned-group", "outline-alice")
	syncEnv(t, kc, ol)

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	assertMembers(t, ol, "orphaned-group", []string{"outline-alice"})
	for _, write := range ol.writeRequests() {
		if write.JSON["id"] == "orphaned-group" {
			t.Fatalf("the service wrote to an Orphaned Managed Group: %+v", write)
		}
	}
}

func TestRunOnceWritesNothingWhenKeycloakReadFails(t *testing.T) {
	kc := newFakeKeycloak(t, []clientRole{{ID: "role-uuid", Name: "Team A"}})
	kc.failNextUserListings(1)
	ol := newFakeOutline(t)
	ol.seedGroups(outlineGroup{
		ID: "managed-group", Name: "Team A", ExternalID: "keycloak:test:roles-client:role-uuid",
	})
	ol.seedUsers(outlineUser{ID: "outline-alice", Email: "alice@example.com"})
	ol.seedMembership("managed-group", "outline-alice")
	syncEnv(t, kc, ol)

	err := app.RunOnce(context.Background())
	if err == nil {
		t.Fatal("expected the Sync Run to fail when Keycloak is unavailable")
	}
	if writes := ol.writeRequests(); len(writes) != 0 {
		t.Fatalf("a failed Keycloak read must abort before any Outline write, got: %+v", writes)
	}
	if requests := ol.allRequests(); len(requests) != 0 {
		t.Fatalf("a failed Keycloak read must abort before touching Outline, got: %+v", requests)
	}
}

func TestRunOnceAppliesOtherOperationsWhenOneFails(t *testing.T) {
	kc := newFakeKeycloak(t, []clientRole{
		{ID: "role-a-uuid", Name: "Team A"},
		{ID: "role-b-uuid", Name: "Team B"},
	})
	kc.seedUsers(
		keycloakUser{
			ID: "kc-alice", Username: "alice", Email: "alice@example.com", Enabled: true,
			DirectRoles: []string{"Team A"},
		},
		keycloakUser{
			ID: "kc-bob", Username: "bob", Email: "bob@example.com", Enabled: true,
			DirectRoles: []string{"Team B"},
		},
	)
	ol := newFakeOutline(t)
	ol.seedUsers(
		outlineUser{ID: "outline-alice", Email: "alice@example.com"},
		outlineUser{ID: "outline-bob", Email: "bob@example.com"},
	)
	ol.failNextAddUser(1)
	syncEnv(t, kc, ol)

	err := app.RunOnce(context.Background())
	if err == nil {
		t.Fatal("expected the Sync Run to report the failed operation")
	}

	assertMembers(t, ol, "created-group-1", nil)
	assertMembers(t, ol, "created-group-2", []string{"outline-bob"})
}

func TestRunOnceTwiceRemovesMemberOnlyOnce(t *testing.T) {
	kc := newFakeKeycloak(t, []clientRole{{ID: "role-uuid", Name: "Team A"}})
	ol := newFakeOutline(t)
	ol.seedGroups(outlineGroup{
		ID: "managed-group", Name: "Team A", ExternalID: "keycloak:test:roles-client:role-uuid",
	})
	ol.seedUsers(outlineUser{ID: "outline-alice", Email: "alice@example.com"})
	ol.seedMembership("managed-group", "outline-alice")
	syncEnv(t, kc, ol)

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("first RunOnce: %v", err)
	}
	assertMembers(t, ol, "managed-group", nil)
	writesAfterFirst := len(ol.writeRequests())
	if writesAfterFirst == 0 {
		t.Fatal("first Sync Run should have removed the extra member")
	}

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("second RunOnce: %v", err)
	}
	if writesAfterSecond := len(ol.writeRequests()); writesAfterSecond != writesAfterFirst {
		t.Fatalf("second Sync Run performed %d extra write(s); membership is already in sync",
			writesAfterSecond-writesAfterFirst)
	}
}

func TestRunOnceWritesNothingWhenRoleMappingReadFails(t *testing.T) {
	kc := newFakeKeycloak(t, []clientRole{{ID: "role-uuid", Name: "Team A"}})
	kc.seedUsers(keycloakUser{
		ID: "kc-alice", Username: "alice", Email: "alice@example.com", Enabled: true,
		DirectRoles: []string{"Team A"},
	})
	kc.failNextRoleMappingListings(1)
	ol := newFakeOutline(t)
	ol.seedGroups(outlineGroup{
		ID: "managed-group", Name: "Team A", ExternalID: "keycloak:test:roles-client:role-uuid",
	})
	ol.seedUsers(outlineUser{ID: "outline-alice", Email: "alice@example.com"})
	ol.seedMembership("managed-group", "outline-alice")
	syncEnv(t, kc, ol)

	err := app.RunOnce(context.Background())
	if err == nil {
		t.Fatal("expected the Sync Run to fail when Keycloak role mappings are unavailable")
	}
	if requests := ol.allRequests(); len(requests) != 0 {
		t.Fatalf("a failed Keycloak read must abort before touching Outline, got: %+v", requests)
	}
}

func TestRunOnceDoesNotRemoveMemberSharingAnEmail(t *testing.T) {
	kc := newFakeKeycloak(t, []clientRole{{ID: "role-uuid", Name: "Team A"}})
	kc.seedUsers(
		keycloakUser{
			ID: "kc-eve-one", Username: "eve-one", Email: "eve@example.com", Enabled: true,
			DirectRoles: []string{"Team A"},
		},
		keycloakUser{
			ID: "kc-eve-two", Username: "eve-two", Email: "eve@example.com", Enabled: true,
			DirectRoles: []string{"Team A"},
		},
	)
	ol := newFakeOutline(t)
	ol.seedGroups(outlineGroup{
		ID: "managed-group", Name: "Team A", ExternalID: "keycloak:test:roles-client:role-uuid",
	})
	ol.seedUsers(outlineUser{ID: "outline-eve", Email: "eve@example.com"})
	ol.seedMembership("managed-group", "outline-eve")
	syncEnv(t, kc, ol)

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	assertMembers(t, ol, "managed-group", []string{"outline-eve"})
	if writes := ol.writeRequests(); len(writes) != 0 {
		t.Fatalf("expected no writes for an ambiguous email, got: %+v", writes)
	}
}

func TestRunOnceReconcilesMembershipWhenGroupUpdateFails(t *testing.T) {
	kc := newFakeKeycloak(t, []clientRole{{ID: "role-uuid", Name: "Team Renamed"}})
	kc.seedUsers(keycloakUser{
		ID: "kc-alice", Username: "alice", Email: "alice@example.com", Enabled: true,
		DirectRoles: []string{"Team Renamed"},
	})
	ol := newFakeOutline(t)
	ol.seedGroups(outlineGroup{
		ID: "managed-group", Name: "Team Old", ExternalID: "keycloak:test:roles-client:role-uuid",
	})
	ol.seedUsers(outlineUser{ID: "outline-alice", Email: "alice@example.com"})
	ol.failNextUpdateGroup(1)
	syncEnv(t, kc, ol)

	err := app.RunOnce(context.Background())
	if err == nil {
		t.Fatal("expected the Sync Run to report the failed rename")
	}

	assertMembers(t, ol, "managed-group", []string{"outline-alice"})
}

func TestRunOnceDryRunWritesNothing(t *testing.T) {
	kc, ol := seedDryRunFixture(t)
	syncEnv(t, kc, ol)
	t.Setenv("DRY_RUN", "true")

	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if writes := ol.writeRequests(); len(writes) != 0 {
		t.Fatalf("dry run must not write to Outline, got: %+v", writes)
	}
	assertGroups(t, ol, []outlineGroup{
		{ID: "managed-a", Name: "Team Old", ExternalID: "keycloak:test:roles-client:role-a-uuid"},
		{ID: "adoptable-c", Name: "team c"},
	})
	assertMembers(t, ol, "managed-a", []string{"outline-alice", "outline-bob"})
}

func TestRunOnceDryRunThenRealRunAppliesPlan(t *testing.T) {
	kc, ol := seedDryRunFixture(t)
	syncEnv(t, kc, ol)

	t.Setenv("DRY_RUN", "true")
	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("dry RunOnce: %v", err)
	}
	if writes := ol.writeRequests(); len(writes) != 0 {
		t.Fatalf("dry run must not write to Outline, got: %+v", writes)
	}

	t.Setenv("DRY_RUN", "false")
	if err := app.RunOnce(context.Background()); err != nil {
		t.Fatalf("real RunOnce: %v", err)
	}

	assertGroups(t, ol, []outlineGroup{
		{ID: "managed-a", Name: "Team A", ExternalID: "keycloak:test:roles-client:role-a-uuid"},
		{ID: "adoptable-c", Name: "Team C", ExternalID: "keycloak:test:roles-client:role-c-uuid"},
		{ID: "created-group-1", Name: "Team B", ExternalID: "keycloak:test:roles-client:role-b-uuid"},
	})
	assertMembers(t, ol, "managed-a", []string{"outline-alice"})
	assertMembers(t, ol, "adoptable-c", []string{"outline-erin"})
	assertMembers(t, ol, "created-group-1", []string{"outline-dave"})
	if writes := ol.writeRequests(); len(writes) != 6 {
		t.Fatalf("expected rename, adopt, create, remove and two adds; got %d write(s): %+v", len(writes), writes)
	}
}
