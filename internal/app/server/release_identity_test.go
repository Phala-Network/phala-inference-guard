package server

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReleaseVersionIsAssignedV01222(t *testing.T) {
	t.Parallel()

	if version != "PIG-v0.12.22" {
		t.Fatalf("runtime version = %q, want the assigned PIG-v0.12.22 identity", version)
	}
}

func TestReleaseVersionMatchesDockerfileOCIImageVersion(t *testing.T) {
	t.Parallel()

	releaseVersion, ok := strings.CutPrefix(version, "PIG-v")
	if !ok || releaseVersion == "" {
		t.Fatalf("runtime version %q does not use the PIG-v<release> contract", version)
	}

	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve release identity test path")
	}
	dockerfilePath := filepath.Join(filepath.Dir(testFile), "..", "..", "..", "Dockerfile")
	dockerfile, err := os.ReadFile(dockerfilePath)
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}

	wantLabel := `org.opencontainers.image.version="` + releaseVersion + `"`
	if !strings.Contains(string(dockerfile), wantLabel) {
		t.Fatalf("Dockerfile OCI version does not match runtime %q; want %s", version, wantLabel)
	}
}
