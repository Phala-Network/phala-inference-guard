//go:build linux && cgo

package attestation

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the real collector in a fresh process so a host NVML library or a
// previous test cannot satisfy dlopen. The fixture records native lifetime and
// keeps the zero-GPU error path deterministic without GPU hardware.
func TestNativeNVMLKeepsLibraryResident(t *testing.T) {
	if os.Getenv("PIG_NVML_LIFETIME_HELPER") == "1" {
		for range 3 {
			_, err := (nativeNVIDIACollector{}).Collect(context.Background(), strings.Repeat("00", 32), "")
			if err == nil || !strings.Contains(err.Error(), "NVML returned zero GPUs") {
				t.Fatalf("collect: %v, want zero-GPU fixture error", err)
			}
		}
		got, err := os.ReadFile(os.Getenv("PIG_NVML_LIFETIME_LOG"))
		if err != nil {
			t.Fatal(err)
		}
		want := "load\ninit\nshutdown\ninit\nshutdown\ninit\nshutdown\n"
		if string(got) != want {
			t.Fatalf("native lifetime = %q, want %q (one load, per-collection init/shutdown, no unload)", got, want)
		}
		return
	}

	compiler, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("native lifetime fixture needs a C compiler")
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "nvml.c")
	fixture := `#include <stdio.h>
#include <stdlib.h>
static void record(const char *event) {
    FILE *f = fopen(getenv("PIG_NVML_LIFETIME_LOG"), "a");
    if (!f) abort();
    fputs(event, f);
    fclose(f);
}
__attribute__((constructor)) static void loaded(void) { record("load\n"); }
__attribute__((destructor)) static void unloaded(void) { record("unload\n"); }
int nvmlInitWithFlags(unsigned int flags) { record("init\n"); return 0; }
int nvmlShutdown(void) { record("shutdown\n"); return 0; }
int nvmlDeviceGetCount_v2(unsigned int *count) { *count = 0; return 0; }
int nvmlDeviceGetHandleByIndex_v2(unsigned int i, void **device) { return 1; }
int nvmlDeviceGetArchitecture(void *device, unsigned int *arch) { return 1; }
int nvmlDeviceGetConfComputeGpuCertificate(void *device, void *cert) { return 1; }
int nvmlDeviceGetConfComputeGpuAttestationReport(void *device, void *report) { return 1; }
`
	if err := os.WriteFile(source, []byte(fixture), 0600); err != nil {
		t.Fatal(err)
	}
	compile := exec.Command(compiler, "-shared", "-fPIC", "-o", filepath.Join(dir, "libnvidia-ml.so.1"), source)
	if out, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("compile NVML fixture: %v\n%s", err, out)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	helper := exec.Command(executable, "-test.run=^TestNativeNVMLKeepsLibraryResident$", "-test.v")
	helper.Env = append(os.Environ(),
		"LD_LIBRARY_PATH="+dir,
		"PIG_NVML_LIFETIME_HELPER=1",
		"PIG_NVML_LIFETIME_LOG="+filepath.Join(dir, "lifetime.log"))
	if out, err := helper.CombinedOutput(); err != nil {
		t.Fatalf("NVML lifetime helper: %v\n%s", err, out)
	}
}
