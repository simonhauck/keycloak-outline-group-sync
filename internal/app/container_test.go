//go:build docker

package app_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const containerImage = "keycloak-outline-group-sync:test"

func buildContainerImage(t *testing.T) {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolving repo root: %v", err)
	}
	command := exec.Command("docker", "build", "-t", containerImage, root)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("docker build: %v\n%s", err, output)
	}
}

func TestContainerImageRunsAsNonRoot(t *testing.T) {
	buildContainerImage(t)

	command := exec.Command("docker", "run", "--rm", "--entrypoint", "id", containerImage)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("docker run id: %v\n%s", err, output)
	}
	if strings.Contains(string(output), "uid=0") {
		t.Fatalf("container runs as root:\n%s", output)
	}
}

func TestContainerImageHasNoBuildToolchain(t *testing.T) {
	buildContainerImage(t)

	command := exec.Command("docker", "run", "--rm", "--entrypoint", "/bin/sh",
		containerImage, "-c", "command -v go || true; command -v gcc || true; command -v cc || true")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("docker run sh: %v\n%s", err, output)
	}
	if strings.TrimSpace(string(output)) != "" {
		t.Fatalf("runtime image contains a build toolchain:\n%s", output)
	}
}

func TestContainerImageRunsOnceAgainstFakes(t *testing.T) {
	buildContainerImage(t)

	kc := newFakeKeycloak(t, []clientRole{{ID: "role-uuid", Name: "Team A"}})
	kc.seedUsers(keycloakUser{
		ID: "kc-alice", Username: "alice", Email: "alice@example.com", Enabled: true,
		DirectRoles: []string{"Team A"},
	})
	ol := newFakeOutline(t)
	ol.seedUsers(outlineUser{ID: "outline-alice", Email: "alice@example.com"})

	args := []string{"run", "--rm", "--network", "host"}
	for _, pair := range serviceEnv(kc, ol) {
		args = append(args, "-e", pair)
	}
	args = append(args, containerImage, "--once")

	command := exec.Command("docker", args...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("docker run --once: %v\n%s", err, output)
	}

	assertGroups(t, ol, []outlineGroup{{
		ID:         "created-group-1",
		Name:       "Team A",
		ExternalID: "keycloak:test:roles-client:role-uuid",
	}})
	assertMembers(t, ol, "created-group-1", []string{"outline-alice"})
}
