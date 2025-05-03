FROM golang:1.24

WORKDIR /go/src/entropych
ENV GOOS=linux GOARCH=amd64
ENV GOCACHE=/go-cache
ENV GOMODCACHE=/gomod-cache

COPY . .
RUN --mount=type=cache,target=/gomod-cache --mount=type=cache,target=/go-cache \
    go build ./cmd/server \
    && go build ./cmd/bots \
    && go build ./cmd/backfill_avatars
