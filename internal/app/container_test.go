//go:build docker

// These tests build and exercise the container image. They need a Linux host
// with Docker and run explicitly: go test -tags docker ./internal/app/.

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

func dockerRun(t *testing.T, args ...string) string {
	t.Helper()
	command := exec.Command("docker", append([]string{"run", "--rm"}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("docker run %v: %v\n%s", args, err, output)
	}
	return string(output)
}

func TestContainerImageRunsAsNonRoot(t *testing.T) {
	buildContainerImage(t)

	output := dockerRun(t, "--entrypoint", "id", containerImage)
	if strings.Contains(output, "uid=0") {
		t.Fatalf("container runs as root:\n%s", output)
	}
}

func TestContainerImageHasNoBuildToolchain(t *testing.T) {
	buildContainerImage(t)

	output := dockerRun(t, "--entrypoint", "/bin/sh", containerImage, "-c",
		"command -v go || true; command -v gcc || true; command -v cc || true")
	if strings.TrimSpace(output) != "" {
		t.Fatalf("runtime image contains a build toolchain:\n%s", output)
	}
}

func TestContainerImageShipsCABundle(t *testing.T) {
	buildContainerImage(t)

	dockerRun(t, "--entrypoint", "ls", containerImage, "/etc/ssl/certs/ca-certificates.crt")
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

	args := []string{"--network", "host"}
	for _, pair := range serviceEnv(kc, ol) {
		args = append(args, "-e", pair)
	}
	args = append(args, containerImage, "--once")
	dockerRun(t, args...)

	assertGroups(t, ol, []outlineGroup{{
		ID:         "created-group-1",
		Name:       "Team A",
		ExternalID: "keycloak:test:roles-client:role-uuid",
	}})
	assertMembers(t, ol, "created-group-1", []string{"outline-alice"})
}
