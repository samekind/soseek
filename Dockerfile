ARG GO_VERSION=1.27.1
FROM node:24-alpine AS web
WORKDIR /web
RUN corepack enable
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/. ./
RUN pnpm build && test -s dist/index.html && test -d dist/assets

FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY schemas ./schemas
COPY --from=web /web/dist ./internal/adminui/dist
RUN GOMAXPROCS=2 GOMEMLIMIT=512MiB CGO_ENABLED=0 go build -p 1 -trimpath -ldflags='-s -w' -o /out/soseekd ./cmd/soseekd && \
    GOMAXPROCS=2 GOMEMLIMIT=512MiB CGO_ENABLED=0 go build -p 1 -trimpath -ldflags='-s -w' -o /out/soseek ./cmd/soseek

FROM alpine:3.23
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -g 10001 soseek && adduser -D -H -u 10001 -G soseek soseek && \
    mkdir -p /var/lib/soseek /etc/soseek/credentials && \
    chown soseek:soseek /var/lib/soseek && chmod 700 /var/lib/soseek
COPY --from=build /out/soseekd /out/soseek /usr/local/bin/
USER 10001:10001
EXPOSE 8080
VOLUME ["/var/lib/soseek"]
ENTRYPOINT ["soseekd"]
CMD ["serve", "--config", "/etc/soseek/config.json"]
