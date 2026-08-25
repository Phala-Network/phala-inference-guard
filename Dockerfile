FROM golang:1.24-bookworm@sha256:1a6d4452c65dea36aac2e2d606b01b4a029ec90cc1ae53890540ce6173ea77ac AS go-build
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build \
        -trimpath \
        -ldflags="-s -w" \
        -o /out/phala-inference-guard \
        ./cmd/phala-inference-guard

FROM gcr.io/distroless/base-debian12@sha256:348dac1808083ccc3366399d6db835875b4eaf7c9b694783f5a3f353c4b58a28
ARG SOURCE_REVISION
LABEL org.opencontainers.image.version="0.12.20" \
      org.opencontainers.image.revision="${SOURCE_REVISION}"
ENV NVIDIA_VISIBLE_DEVICES=all
COPY --from=go-build /out/phala-inference-guard /phala-inference-guard
EXPOSE 8000
ENTRYPOINT ["/phala-inference-guard"]
