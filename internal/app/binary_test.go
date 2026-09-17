package app_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

var serviceBinary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "keycloak-outline-group-sync-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "creating temp dir:", err)
		os.Exit(1)
	}
	serviceBinary = filepath.Join(dir, "service")
	build := exec.Command("go", "build", "-o", serviceBinary,
		"github.com/simonhauck/keycloak-outline-group-sync/cmd/keycloak-outline-group-sync")
	if output, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building service: %v\n%s", err, output)
		os.RemoveAll(dir)
		os.Exit(1)
	}

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func serviceEnv(kc *fakeKeycloak, ol *fakeOutline) []string {
	return []string{
		"KEYCLOAK_URL=" + kc.URL(),
		"KEYCLOAK_REALM=" + testRealm,
		"KEYCLOAK_CLIENT_ID=" + testClientID,
		"KEYCLOAK_CLIENT_SECRET=" + testClientSecret,
		"KEYCLOAK_ROLES_CLIENT_ID=" + testRolesClient,
		"OUTLINE_URL=" + ol.URL(),
		"OUTLINE_TOKEN=" + testOutlineToken,
		"LOG_LEVEL=error",
	}
}

func TestServiceBinaryRunsOnceAndExitsZero(t *testing.T) {
	kc := newFakeKeycloak(t, []clientRole{{ID: "role-uuid", Name: "Team A"}})
	ol := newFakeOutline(t)

	command := exec.Command(serviceBinary, "--once")
	command.Env = serviceEnv(kc, ol)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("service exited with error: %v\n%s", err, output)
	}

	assertGroups(t, ol, []outlineGroup{{
		ID:         "created-group-1",
		Name:       "Team A",
		ExternalID: "keycloak:test:roles-client:role-uuid",
	}})
}

func TestServiceBinaryFailsFastOnMissingConfiguration(t *testing.T) {
	kc := newFakeKeycloak(t, nil)
	ol := newFakeOutline(t)

	command := exec.Command(serviceBinary, "--once")
	command.Env = append(serviceEnv(kc, ol), "OUTLINE_TOKEN=")
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("expected a non-zero exit\n%s", output)
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("service did not exit non-zero: %v", err)
	}
	if !strings.Contains(string(output), "OUTLINE_TOKEN") {
		t.Fatalf("output does not name the missing setting:\n%s", output)
	}
}

func TestServiceBinaryLogsFailedRunAndStopsOnSigterm(t *testing.T) {
	kc := newFakeKeycloak(t, nil)
	ol := newFakeOutline(t)
	ol.failNextListGroups(1)

	command := exec.Command(serviceBinary)
	command.Env = append(serviceEnv(kc, ol), "SYNC_INTERVAL=10ms")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatalf("starting service: %v", err)
	}

	deadline := time.After(5 * time.Second)
	for ol.listCalls() < 2 {
		select {
		case <-deadline:
			command.Process.Kill()
			_ = command.Wait()
			t.Fatalf("service did not continue after a failed Sync Run; stderr:\n%s", stderr.String())
		case <-time.After(5 * time.Millisecond):
		}
	}

	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("sending SIGTERM: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("service did not exit cleanly on SIGTERM: %v\nstderr:\n%s", err, stderr.String())
		}
	case <-time.After(5 * time.Second):
		command.Process.Kill()
		<-done
		t.Fatalf("service did not exit within 5s of SIGTERM; stderr:\n%s", stderr.String())
	}

	if !strings.Contains(stderr.String(), "sync run failed") {
		t.Fatalf("stderr does not log the failed Sync Run:\n%s", stderr.String())
	}
}

func TestServiceBinaryOnceExitsNonZeroOnFailedRun(t *testing.T) {
	kc := newFakeKeycloak(t, nil)
	ol := newFakeOutline(t)
	ol.failNextListGroups(1)

	command := exec.Command(serviceBinary, "--once")
	command.Env = serviceEnv(kc, ol)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("expected a non-zero exit for a failed Sync Run\n%s", output)
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("service did not exit non-zero: %v", err)
	}
}

func envWith(env []string, pair string) []string {
	key, _, _ := strings.Cut(pair, "=")
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, key+"=") {
			out = append(out, entry)
		}
	}
	return append(out, pair)
}

func TestServiceBinaryWarnsAboutSkippedUser(t *testing.T) {
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
	ol.seedUsers(outlineUser{ID: "outline-eve", Email: "eve@example.com"})

	command := exec.Command(serviceBinary, "--once")
	command.Env = envWith(serviceEnv(kc, ol), "LOG_LEVEL=warn")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("service exited with error: %v\n%s", err, output)
	}

	if !strings.Contains(string(output), "skipping Keycloak user") || !strings.Contains(string(output), "duplicate email") {
		t.Fatalf("output does not warn about the skipped user:\n%s", output)
	}
	assertMembers(t, ol, "created-group-1", nil)
}

func TestServiceBinaryReportsRunSummary(t *testing.T) {
	kc := newFakeKeycloak(t, []clientRole{{ID: "role-uuid", Name: "Team A"}})
	kc.seedUsers(keycloakUser{
		ID: "kc-carol", Username: "carol", Email: "carol@example.com", Enabled: false,
		DirectRoles: []string{"Team A"},
	})
	ol := newFakeOutline(t)
	ol.seedGroups(
		outlineGroup{ID: "managed-group", Name: "Team A", ExternalID: "keycloak:test:roles-client:role-uuid"},
		outlineGroup{ID: "orphaned-group", Name: "Old Team", ExternalID: "keycloak:test:roles-client:deleted-role-uuid"},
	)
	ol.seedUsers(outlineUser{ID: "outline-bob", Email: "bob@example.com"})
	ol.seedMembership("managed-group", "outline-bob")

	command := exec.Command(serviceBinary, "--once")
	command.Env = envWith(serviceEnv(kc, ol), "LOG_LEVEL=info")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("service exited with error: %v\n%s", err, output)
	}

	for _, want := range []string{
		`"membersRemoved":1`,
		`"skippedUsers":1`,
		`"orphanedGroups":1`,
		`"failures":0`,
		"orphaned Managed Group",
	} {
		if !strings.Contains(string(output), want) {
			t.Fatalf("run summary does not report %s:\n%s", want, output)
		}
	}
}

func TestServiceBinaryReportsFailedOperation(t *testing.T) {
	kc := newFakeKeycloak(t, []clientRole{{ID: "role-uuid", Name: "Team A"}})
	kc.seedUsers(keycloakUser{
		ID: "kc-alice", Username: "alice", Email: "alice@example.com", Enabled: true,
		DirectRoles: []string{"Team A"},
	})
	ol := newFakeOutline(t)
	ol.seedUsers(outlineUser{ID: "outline-alice", Email: "alice@example.com"})
	ol.failNextAddUser(1)

	command := exec.Command(serviceBinary, "--once")
	command.Env = envWith(serviceEnv(kc, ol), "LOG_LEVEL=info")
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("expected a non-zero exit when an operation fails\n%s", output)
	}
	if !strings.Contains(string(output), `"failures":1`) {
		t.Fatalf("run summary does not report the failure:\n%s", output)
	}
}
