# Build the manager binary.
#
# Multi-stage. The vendored Harbor chart tarball under charts/ is copied
# into the final image at /charts/ so the manager can load it offline
# (no runtime download from helm.goharbor.io).
#
# --platform=$BUILDPLATFORM keeps the build stage native (no QEMU). Go
# cross-compiles to TARGETOS/TARGETARCH via env vars; this avoids QEMU
# segfaults during `go mod download` of large dep trees (Helm SDK).

FROM --platform=$BUILDPLATFORM golang:1.24 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

# Cache deps in a dedicated layer.
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

# Copy the Go source (.dockerignore filters out test data, bin/, etc.).
COPY . .

# Build the manager — TARGETOS/TARGETARCH from buildx for cross-arch.
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o manager cmd/main.go

# Distroless static + nonroot for the runtime image.
FROM gcr.io/distroless/static:nonroot

# OCI image labels — make registries / SBOM tooling happy.
LABEL org.opencontainers.image.title="registry-operator" \
      org.opencontainers.image.description="Harbor registry operator for the WSO2 Open Cloud Datacenter." \
      org.opencontainers.image.source="https://github.com/wso2/open-cloud-datacenter" \
      org.opencontainers.image.licenses="Apache-2.0"

WORKDIR /
COPY --from=builder /workspace/manager .
# Ship the vendored Harbor chart inside the image so the operator never
# fetches from helm.goharbor.io at reconcile time.
COPY charts/ /charts/

USER 65532:65532

ENTRYPOINT ["/manager"]
