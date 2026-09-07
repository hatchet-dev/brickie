FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY api ./api
COPY internal ./internal
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /brickie ./cmd/brickie

FROM alpine:3.22
RUN apk add --no-cache tar
COPY --from=build /brickie /brickie
ENTRYPOINT ["/brickie"]
CMD ["controller"]
