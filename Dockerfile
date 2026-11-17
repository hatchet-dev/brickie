FROM --platform=$BUILDPLATFORM golang:1.27.3 AS build
ARG TARGETARCH
ARG VERSION=
ARG COMMIT=
ARG DATE=
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY hack/check-fips.sh ./hack/
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH GOFIPS140=v1.0.0 \
    go build -ldflags="-s -w -X main.version=$VERSION -X main.commit=$COMMIT -X main.date=$DATE" -o /brickie ./cmd/brickie \
    && hack/check-fips.sh /brickie

FROM gcr.io/distroless/static-debian12:nonroot
LABEL run.hatchet.fips=true
COPY --from=build /brickie /brickie
ENTRYPOINT ["/brickie"]
CMD ["worker"]
