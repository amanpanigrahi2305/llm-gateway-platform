# syntax=docker/dockerfile:1
# Two small images from one build: the gateway and the mock provider.
#   docker build --target gateway      -t llm-gateway:dev .
#   docker build --target mockprovider -t llm-gateway-mock:dev .
# Multi-arch: add --platform linux/amd64,linux/arm64 (docker buildx).

# The build stage always runs on the build machine's own platform. The Go
# compiler cross-compiles for the target, so arm64 images need no emulation.
FROM --platform=$BUILDPLATFORM golang:1.27.4-alpine AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
# CGO_ENABLED=0 gives a static binary: it needs no C library in the image.
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w -X main.version=$VERSION" -o /out/gateway ./cmd/gateway && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w" -o /out/mockprovider ./cmd/mockprovider

# distroless/static has no shell and no package manager: only CA certificates,
# time zone data and /etc/passwd. Less software means fewer CVEs and nothing
# for an attacker to run. The :nonroot tag already runs as user 65532; USER
# says it again so scanners and readers see it.
FROM gcr.io/distroless/static-debian13:nonroot AS mockprovider
COPY --from=build /out/mockprovider /mockprovider
USER 65532:65532
EXPOSE 9090
ENTRYPOINT ["/mockprovider"]

# The last stage is the default target.
FROM gcr.io/distroless/static-debian13:nonroot AS gateway
COPY --from=build /out/gateway /gateway
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/gateway"]
CMD ["serve", "-config", "/etc/llm-gateway/config.yaml"]
