FROM golang:1.24-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/phala-inference-guard ./cmd/phala-inference-guard

FROM gcr.io/distroless/base-debian12
ENV NVIDIA_VISIBLE_DEVICES=all
COPY --from=build /out/phala-inference-guard /phala-inference-guard
EXPOSE 8000
ENTRYPOINT ["/phala-inference-guard"]
