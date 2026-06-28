//go:build linux && cgo

package attestation

/*
#cgo LDFLAGS: -ldl
#include <dlfcn.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

#define PIG_NVML_GPU_CERT_CHAIN_SIZE 0x1000
#define PIG_NVML_GPU_ATTESTATION_CERT_CHAIN_SIZE 0x1400
#define PIG_NVML_CC_GPU_CEC_NONCE_SIZE 0x20
#define PIG_NVML_CC_GPU_ATTESTATION_REPORT_SIZE 0x2000
#define PIG_NVML_CC_GPU_CEC_ATTESTATION_REPORT_SIZE 0x1000

typedef void* nvmlDevice_t;

typedef struct {
	unsigned int certChainSize;
	unsigned int attestationCertChainSize;
	unsigned char certChain[PIG_NVML_GPU_CERT_CHAIN_SIZE];
	unsigned char attestationCertChain[PIG_NVML_GPU_ATTESTATION_CERT_CHAIN_SIZE];
} pig_nvmlConfComputeGpuCertificate_t;

typedef struct {
	unsigned int isCecAttestationReportPresent;
	unsigned int attestationReportSize;
	unsigned int cecAttestationReportSize;
	unsigned char nonce[PIG_NVML_CC_GPU_CEC_NONCE_SIZE];
	unsigned char attestationReport[PIG_NVML_CC_GPU_ATTESTATION_REPORT_SIZE];
	unsigned char cecAttestationReport[PIG_NVML_CC_GPU_CEC_ATTESTATION_REPORT_SIZE];
} pig_nvmlConfComputeGpuAttestationReport_t;

typedef int (*pig_nvmlInitWithFlags_t)(unsigned int);
typedef int (*pig_nvmlShutdown_t)(void);
typedef int (*pig_nvmlDeviceGetCount_v2_t)(unsigned int*);
typedef int (*pig_nvmlDeviceGetHandleByIndex_v2_t)(unsigned int, nvmlDevice_t*);
typedef int (*pig_nvmlDeviceGetArchitecture_t)(nvmlDevice_t, unsigned int*);
typedef int (*pig_nvmlDeviceGetConfComputeGpuCertificate_t)(nvmlDevice_t, pig_nvmlConfComputeGpuCertificate_t*);
typedef int (*pig_nvmlDeviceGetConfComputeGpuAttestationReport_t)(nvmlDevice_t, pig_nvmlConfComputeGpuAttestationReport_t*);

typedef struct {
	void* lib;
	pig_nvmlInitWithFlags_t initWithFlags;
	pig_nvmlShutdown_t shutdown;
	pig_nvmlDeviceGetCount_v2_t getCount;
	pig_nvmlDeviceGetHandleByIndex_v2_t getHandleByIndex;
	pig_nvmlDeviceGetArchitecture_t getArchitecture;
	pig_nvmlDeviceGetConfComputeGpuCertificate_t getCertificate;
	pig_nvmlDeviceGetConfComputeGpuAttestationReport_t getAttestationReport;
} pig_nvml_t;

static void* pig_dlsym(void* lib, const char* name, const char** missing) {
	void* sym = dlsym(lib, name);
	if (sym == NULL && missing != NULL && *missing == NULL) {
		*missing = name;
	}
	return sym;
}

static const char* pig_nvml_open(pig_nvml_t* n) {
	memset(n, 0, sizeof(*n));
	n->lib = dlopen("libnvidia-ml.so.1", RTLD_LAZY);
	if (n->lib == NULL) {
		const char* err = dlerror();
		return err == NULL ? "dlopen libnvidia-ml.so.1 failed" : err;
	}

	const char* missing = NULL;
	n->initWithFlags = (pig_nvmlInitWithFlags_t)pig_dlsym(n->lib, "nvmlInitWithFlags", &missing);
	n->shutdown = (pig_nvmlShutdown_t)pig_dlsym(n->lib, "nvmlShutdown", &missing);
	n->getCount = (pig_nvmlDeviceGetCount_v2_t)pig_dlsym(n->lib, "nvmlDeviceGetCount_v2", &missing);
	n->getHandleByIndex = (pig_nvmlDeviceGetHandleByIndex_v2_t)pig_dlsym(n->lib, "nvmlDeviceGetHandleByIndex_v2", &missing);
	n->getArchitecture = (pig_nvmlDeviceGetArchitecture_t)pig_dlsym(n->lib, "nvmlDeviceGetArchitecture", &missing);
	n->getCertificate = (pig_nvmlDeviceGetConfComputeGpuCertificate_t)pig_dlsym(n->lib, "nvmlDeviceGetConfComputeGpuCertificate", &missing);
	n->getAttestationReport = (pig_nvmlDeviceGetConfComputeGpuAttestationReport_t)pig_dlsym(n->lib, "nvmlDeviceGetConfComputeGpuAttestationReport", &missing);
	if (missing != NULL) {
		dlclose(n->lib);
		memset(n, 0, sizeof(*n));
		return missing;
	}
	return NULL;
}

static void pig_nvml_close(pig_nvml_t* n) {
	if (n->lib != NULL) {
		dlclose(n->lib);
	}
	memset(n, 0, sizeof(*n));
}

static int pig_nvml_init(pig_nvml_t* n) {
	return n->initWithFlags(0);
}

static int pig_nvml_shutdown(pig_nvml_t* n) {
	return n->shutdown();
}

static int pig_nvml_get_count(pig_nvml_t* n, unsigned int* count) {
	return n->getCount(count);
}

static int pig_nvml_get_handle(pig_nvml_t* n, unsigned int index, nvmlDevice_t* device) {
	return n->getHandleByIndex(index, device);
}

static int pig_nvml_get_arch(pig_nvml_t* n, nvmlDevice_t device, unsigned int* arch) {
	return n->getArchitecture(device, arch);
}

static int pig_nvml_get_cert(pig_nvml_t* n, nvmlDevice_t device, pig_nvmlConfComputeGpuCertificate_t* cert) {
	memset(cert, 0, sizeof(*cert));
	return n->getCertificate(device, cert);
}

static int pig_nvml_get_report(pig_nvml_t* n, nvmlDevice_t device, const unsigned char* nonce, pig_nvmlConfComputeGpuAttestationReport_t* report) {
	memset(report, 0, sizeof(*report));
	memcpy(report->nonce, nonce, PIG_NVML_CC_GPU_CEC_NONCE_SIZE);
	return n->getAttestationReport(device, report);
}
*/
import "C"

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"unsafe"
)

type nativeNVIDIACollector struct{}

func newNativeNVIDIACollector() nvidiaCollector {
	return nativeNVIDIACollector{}
}

func (nativeNVIDIACollector) Collect(ctx context.Context, nonceHex string, fallbackArch string) (string, error) {
	nonce, err := hex.DecodeString(nonceHex)
	if err != nil {
		return "", fmt.Errorf("nvidia nonce must be hex-encoded: %w", err)
	}
	if len(nonce) != C.PIG_NVML_CC_GPU_CEC_NONCE_SIZE {
		return "", fmt.Errorf("nvidia nonce must be 32 bytes")
	}

	var nvml C.pig_nvml_t
	if errMsg := C.pig_nvml_open(&nvml); errMsg != nil {
		return "", fmt.Errorf("open NVML: %s", C.GoString(errMsg))
	}
	defer C.pig_nvml_close(&nvml)

	if ret := C.pig_nvml_init(&nvml); ret != 0 {
		return "", fmt.Errorf("nvmlInitWithFlags failed with code %d", int(ret))
	}
	defer C.pig_nvml_shutdown(&nvml)

	var count C.uint
	if ret := C.pig_nvml_get_count(&nvml, &count); ret != 0 {
		return "", fmt.Errorf("nvmlDeviceGetCount_v2 failed with code %d", int(ret))
	}
	if count == 0 {
		return "", fmt.Errorf("NVML returned zero GPUs")
	}

	evidences := make([]nvidiaEvidence, 0, int(count))
	for i := C.uint(0); i < count; i++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		evidence, err := collectNVIDIAEvidenceForGPU(&nvml, i, nonce)
		if err != nil {
			return "", err
		}
		evidences = append(evidences, evidence)
	}
	return marshalNVIDIAPayload(nonceHex, evidences, fallbackArch)
}

func collectNVIDIAEvidenceForGPU(nvml *C.pig_nvml_t, index C.uint, nonce []byte) (nvidiaEvidence, error) {
	var device C.nvmlDevice_t
	if ret := C.pig_nvml_get_handle(nvml, index, &device); ret != 0 {
		return nvidiaEvidence{}, fmt.Errorf("nvmlDeviceGetHandleByIndex_v2(%d) failed with code %d", uint(index), int(ret))
	}

	var archRaw C.uint
	if ret := C.pig_nvml_get_arch(nvml, device, &archRaw); ret != 0 {
		return nvidiaEvidence{}, fmt.Errorf("nvmlDeviceGetArchitecture(%d) failed with code %d", uint(index), int(ret))
	}
	arch, ok := nvidiaArchName(uint32(archRaw))
	if !ok {
		return nvidiaEvidence{}, fmt.Errorf("unsupported NVIDIA GPU architecture %d", uint32(archRaw))
	}

	var cert C.pig_nvmlConfComputeGpuCertificate_t
	if ret := C.pig_nvml_get_cert(nvml, device, &cert); ret != 0 {
		return nvidiaEvidence{}, fmt.Errorf("nvmlDeviceGetConfComputeGpuCertificate(%d) failed with code %d", uint(index), int(ret))
	}
	if cert.attestationCertChainSize == 0 || cert.attestationCertChainSize > C.PIG_NVML_GPU_ATTESTATION_CERT_CHAIN_SIZE {
		return nvidiaEvidence{}, fmt.Errorf("invalid NVIDIA attestation certificate chain size %d", uint(cert.attestationCertChainSize))
	}
	certBytes := C.GoBytes(unsafe.Pointer(&cert.attestationCertChain[0]), C.int(cert.attestationCertChainSize))
	certBase64, err := encodeNVIDIACertificateChain(certBytes)
	if err != nil {
		return nvidiaEvidence{}, err
	}

	var report C.pig_nvmlConfComputeGpuAttestationReport_t
	if ret := C.pig_nvml_get_report(nvml, device, (*C.uchar)(unsafe.Pointer(&nonce[0])), &report); ret != 0 {
		return nvidiaEvidence{}, fmt.Errorf("nvmlDeviceGetConfComputeGpuAttestationReport(%d) failed with code %d", uint(index), int(ret))
	}
	if report.attestationReportSize == 0 || report.attestationReportSize > C.PIG_NVML_CC_GPU_ATTESTATION_REPORT_SIZE {
		return nvidiaEvidence{}, fmt.Errorf("invalid NVIDIA attestation report size %d", uint(report.attestationReportSize))
	}
	reportBytes := C.GoBytes(unsafe.Pointer(&report.attestationReport[0]), C.int(report.attestationReportSize))

	return nvidiaEvidence{
		Certificate: certBase64,
		Evidence:    base64.StdEncoding.EncodeToString(reportBytes),
		Arch:        arch,
	}, nil
}
