#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
cd "$repo_root"

dockerfile=${PIG_DOCKERFILE:-Dockerfile}
image=${PIG_IMAGE_UNDER_TEST:-pig-production-contract:local}
expected_version=${EXPECTED_VERSION:-}
expected_label_version=${expected_version#v}

fail() {
    printf '%s\n' "PIG production image contract failed: $*" >&2
    exit 1
}

grep -Eq 'CGO_ENABLED=1([[:space:]]|\\)' "$dockerfile" ||
    fail 'Dockerfile must compile the production binary with CGO_ENABLED=1 for the native NVIDIA collector'

grep -Eq '^FROM[[:space:]]+gcr\.io/distroless/base-debian12(@sha256:[0-9a-f]+)?([[:space:]]|$)' "$dockerfile" ||
    fail 'production runtime must use distroless base-debian12 rather than a static image'

if [ -z "${PIG_IMAGE_UNDER_TEST:-}" ]; then
    docker build --pull=false --tag "$image" --file "$dockerfile" .
fi

label_version=$(docker image inspect --format '{{ index .Config.Labels "org.opencontainers.image.version" }}' "$image")
[ -n "$label_version" ] && [ "$label_version" != '<no value>' ] ||
    fail 'image is missing org.opencontainers.image.version'
if [ -n "$expected_version" ] && [ "$label_version" != "$expected_label_version" ]; then
    fail "image label version $label_version does not match expected tag $expected_version"
fi

visible_devices=$(docker image inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$image" |
    grep '^NVIDIA_VISIBLE_DEVICES=' || true)
[ "$visible_devices" = 'NVIDIA_VISIBLE_DEVICES=all' ] ||
    fail 'image must request NVIDIA devices so libnvidia-ml.so.1 can be injected at runtime'

tmp_dir=$(mktemp -d)
container_id=''
cleanup() {
    if [ -n "$container_id" ]; then
        docker rm -f "$container_id" >/dev/null 2>&1 || true
    fi
    rm -rf "$tmp_dir"
}
trap cleanup EXIT INT TERM

container_id=$(docker create "$image")
docker cp "$container_id:/phala-inference-guard" "$tmp_dir/phala-inference-guard"
docker rm "$container_id" >/dev/null
container_id=''

if grep -aFq 'native NVIDIA collector requires linux with cgo and NVML' "$tmp_dir/phala-inference-guard"; then
    fail 'image binary contains the non-cgo NVIDIA collector stub'
fi
grep -aFq 'open NVML:' "$tmp_dir/phala-inference-guard" ||
    fail 'image binary does not contain the native NVML collector path'

printf '%s\n' "PIG_PRODUCTION_IMAGE_CONTRACT_OK image=$image version=$label_version"
