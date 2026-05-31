ARG GO_VERSION=1.25

FROM golang:${GO_VERSION}-alpine AS build
RUN apk add --no-cache build-base git
WORKDIR /src
COPY tools/api/go.mod tools/api/go.sum ./
RUN go mod download
COPY tools/api/ ./
# CGO is not needed (pure-Go pgx driver), so build a static binary.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/api /usr/local/bin/api
ENTRYPOINT ["/usr/local/bin/api"]
