ARG GO_VERSION=1.25

FROM golang:${GO_VERSION}-alpine AS build
RUN apk add --no-cache build-base git
WORKDIR /src
COPY tools/gdocs-indexer/go.mod tools/gdocs-indexer/go.sum ./
RUN go mod download
COPY tools/gdocs-indexer/ ./
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/gdocs-indexer ./cmd/gdocs-indexer

FROM alpine:3.20
RUN apk add --no-cache ca-certificates sqlite tzdata
COPY --from=build /out/gdocs-indexer /usr/local/bin/gdocs-indexer
ENTRYPOINT ["/usr/local/bin/gdocs-indexer"]
