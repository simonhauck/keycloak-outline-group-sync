package app_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
