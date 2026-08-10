# Build the static Go binary with the committed UI embedded. The web UI lives
# in internal/server/uidist/ (committed, embedded via go:embed); the ui-drift CI
# job rebuilds it and fails if that committed output is stale, so the image can
# trust it without a node build stage of its own.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/llmproxy ./cmd/llmproxy

FROM alpine:3.21
RUN adduser -D -u 1000 llmproxy && mkdir -p /data && chown llmproxy:llmproxy /data
COPY --from=build /out/llmproxy /usr/local/bin/llmproxy
USER llmproxy
VOLUME /data
ENV LLMPROXY_DATABASE_URL=/data/llmproxy.db \
    LLMPROXY_SECRET_FILE=/data/secret \
    LLMPROXY_ADMIN_PASSWORD_FILE=/data/admin-password \
    LLMPROXY_HOST=0.0.0.0 \
    LLMPROXY_PORT=4000
EXPOSE 4000
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -q -O /dev/null "http://127.0.0.1:${LLMPROXY_PORT:-4000}/healthz" || exit 1
ENTRYPOINT ["llmproxy"]
CMD ["serve"]
