ARG GO_VERSION=1.24

FROM golang:${GO_VERSION}-alpine AS build
RUN apk add --no-cache build-base git
WORKDIR /src
COPY tools/mail-indexer/go.mod tools/mail-indexer/go.sum ./
RUN go mod download
COPY tools/mail-indexer/ ./
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/mail-indexer ./cmd/mail-indexer

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/mail-indexer /usr/local/bin/mail-indexer
ENTRYPOINT ["/usr/local/bin/mail-indexer"]
