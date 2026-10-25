FROM --platform=$BUILDPLATFORM golang:1.27.3 AS build
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -ldflags="-s -w" -o /brickie ./cmd/brickie

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /brickie /brickie
ENTRYPOINT ["/brickie"]
CMD ["worker"]
