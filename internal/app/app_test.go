package app_test

import (
	"context"
	"fmt"
	"reflect"
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

	runServiceUntil(t, app.Run, ol.listCalls, 2)

	assertGroups(t, ol, []outlineGroup{{
		ID:         "created-group-1",
		Name:       "Team A",
		ExternalID: "keycloak:test:roles-client:role-uuid",
	}})
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
