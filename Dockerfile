FROM --platform=$BUILDPLATFORM golang:1.27.3 AS build
ARG TARGETARCH
ARG VERSION=
ARG COMMIT=
ARG DATE=
ARG FIPS=false
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY hack/check-fips.sh ./hack/
RUN if [ "$FIPS" = "true" ]; then export GOFIPS140=v1.0.0; fi \
    && CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH \
    go build -ldflags="-s -w -X main.version=$VERSION -X main.commit=$COMMIT -X main.date=$DATE" -o /brickie ./cmd/brickie \
    && if [ "$FIPS" = "true" ]; then hack/check-fips.sh /brickie; fi

FROM gcr.io/distroless/static-debian12:nonroot
ARG FIPS=false
LABEL run.hatchet.fips=$FIPS
COPY --from=build /brickie /brickie
ENTRYPOINT ["/brickie"]
CMD ["worker"]
