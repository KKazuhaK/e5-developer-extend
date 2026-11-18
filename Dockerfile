# syntax=docker/dockerfile:1
# The build stage runs on the build machine and cross-compiles, so a
# multi-platform build (linux/amd64, linux/arm64) needs no emulation.
# Pinned: the golang image forces GOTOOLCHAIN=local, so go.mod's toolchain line is ignored here.
FROM --platform=$BUILDPLATFORM golang:1.26.11@sha256:4165ee6bf3b7811fd7b40373290405c83d0167dcb87ee5f29c427cdcef45ff8f AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
ARG TARGETOS=linux
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags="-s -w \
      -X github.com/KKazuhaK/e5-developer-extend/internal/version.Version=${VERSION} \
      -X github.com/KKazuhaK/e5-developer-extend/internal/version.Commit=${COMMIT} \
      -X github.com/KKazuhaK/e5-developer-extend/internal/version.Date=${DATE}" \
    -o /out/e5extend ./cmd/e5extend
# The final image has no shell, so the data directories are created here and
# copied over with the right owner (65532 is distroless "nonroot").
RUN mkdir -p /out/data/config /out/data/state

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/e5extend /e5extend
COPY --from=build --chown=65532:65532 /out/data /data
ENV E5EXTEND_CONFIG_DIR=/data/config \
    E5EXTEND_DATA_DIR=/data/state
VOLUME ["/data"]
USER 65532:65532
ENTRYPOINT ["/e5extend"]
CMD ["run", "--log-format", "json"]
