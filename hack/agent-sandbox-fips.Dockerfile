FROM --platform=$BUILDPLATFORM golang:1.27.3 AS build
ARG TARGETARCH
ARG VERSION
WORKDIR /src
RUN git clone --depth 1 --branch "$VERSION" https://github.com/kubernetes-sigs/agent-sandbox.git . && go mod download
COPY check-fips.sh /check-fips.sh
ENV CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH GOFIPS140=v1.0.0 GOTOOLCHAIN=local
RUN go build -ldflags="-s -w -X sigs.k8s.io/agent-sandbox/internal/version.gitVersion=$VERSION" -o /out/sandboxd ./packages/sandboxd/cmd/sandboxd \
    && go build -ldflags="-s -w -X sigs.k8s.io/agent-sandbox/internal/version.gitVersion=$VERSION" -o /out/agent-sandbox-controller ./cmd/agent-sandbox-controller \
    && /check-fips.sh /out/sandboxd && /check-fips.sh /out/agent-sandbox-controller

FROM gcr.io/distroless/static-debian13:nonroot AS controller
COPY --from=build /out/agent-sandbox-controller /agent-sandbox-controller
ENTRYPOINT ["/agent-sandbox-controller"]

FROM debian:bookworm-slim AS sandboxd
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates procps && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /workspace && chown 1000:1000 /workspace
COPY --from=build /out/sandboxd /usr/local/bin/sandboxd
WORKDIR /workspace
USER 1000
ENTRYPOINT ["/usr/local/bin/sandboxd"]
