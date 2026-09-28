# Contributing

Start a focused branch from `main`. Describe the problem, resulting behavior,
and relevant validation in your pull request. Keep source, tests and current
configuration/API documentation consistent; avoid committing local evidence,
credentials, generated binaries or duplicate implementations.

## Checks

Use Go 1.24 or later; run production validation on Linux with CGO enabled.
Package/test serialization helps avoid timing interference on small builders;
concurrent tests still exercise their goroutines.

```bash
go test ./... -p=1 -parallel=1
go test -race ./... -p=1 -parallel=1
go vet ./...
go build ./cmd/phala-inference-guard
```

Format changed Go files with `gofmt`. Admission changes need regression coverage
for atomic decisions, reservations, cancellation and affected protocol behavior.
Document environmental limits or failed checks rather than weakening assertions.

Merge validated changes into `main`. Keep maintenance branches only for active
compatibility needs; preserve published source with immutable tags. See
[release guidance](docs/RELEASING.md). Repository work does not imply deployment.
