FROM rust:1.97-bookworm@sha256:77fac8b98f9f46062bb680b6d25d5bcaabfc400143952ebc572e924bcbedc3fa AS tokenizer-build
WORKDIR /src/native/tokenizer
COPY native/tokenizer/Cargo.toml native/tokenizer/Cargo.lock ./
COPY native/tokenizer/src ./src
RUN cargo build --locked --release --lib \
    && install -D -m 0755 target/release/libpig_tokenizer_native.so /out/libpig_tokenizer_native.so

FROM golang:1.24-bookworm@sha256:1a6d4452c65dea36aac2e2d606b01b4a029ec90cc1ae53890540ce6173ea77ac AS go-build
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd ./cmd
COPY internal ./internal
COPY native/tokenizer/include ./native/tokenizer/include
COPY --from=tokenizer-build /out/libpig_tokenizer_native.so /usr/local/lib/libpig_tokenizer_native.so
RUN ldconfig \
    && CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build \
        -tags=pig_native \
        -trimpath \
        -ldflags="-s -w" \
        -o /out/phala-inference-guard \
        ./cmd/phala-inference-guard

FROM gcr.io/distroless/base-debian12@sha256:348dac1808083ccc3366399d6db835875b4eaf7c9b694783f5a3f353c4b58a28
LABEL org.opencontainers.image.version="0.9.3"
ENV NVIDIA_VISIBLE_DEVICES=all
ENV LD_LIBRARY_PATH=/usr/lib
COPY --from=go-build /out/phala-inference-guard /phala-inference-guard
COPY --from=tokenizer-build /out/libpig_tokenizer_native.so /usr/lib/libpig_tokenizer_native.so
COPY --from=tokenizer-build /lib/x86_64-linux-gnu/libgcc_s.so.1 /lib/x86_64-linux-gnu/libgcc_s.so.1
EXPOSE 8000
ENTRYPOINT ["/phala-inference-guard"]
