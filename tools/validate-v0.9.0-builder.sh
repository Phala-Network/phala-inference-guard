#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
cd "$repo_root"

go_bin=${GO_BIN:-/usr/local/go/bin/go}
gofmt_bin=${GOFMT_BIN:-/usr/local/go/bin/gofmt}

unformatted=$(find cmd internal -type f -name '*.go' -exec "$gofmt_bin" -l {} +)
if [ -n "$unformatted" ]; then
    printf '%s\n' "gofmt check failed:" "$unformatted" >&2
    exit 1
fi

"$go_bin" test ./...
"$go_bin" test -race ./...
"$go_bin" run ./cmd/pig-kv-sim -scenario scenarios/kv-admission -all
"$go_bin" run ./cmd/pig-kv-sim -performance
"$go_bin" build ./cmd/phala-inference-guard ./cmd/pig-kv-sim

printf '%s\n' 'PIG_V090_BUILDER_VALIDATION_OK'
