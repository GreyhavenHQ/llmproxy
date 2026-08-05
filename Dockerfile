# Stage 1: build the web UI from source (the repo also commits the built
# uidist/, but the image builds it fresh so the two cannot drift).
FROM node:24-alpine AS ui
WORKDIR /src
COPY ui/package.json ui/package-lock.json ui/
RUN cd ui && npm ci --no-fund --no-audit
COPY ui/ ui/
RUN mkdir -p internal/server && cd ui && npm run build

# Stage 2: build the static Go binary with the UI embedded.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui /src/internal/server/uidist internal/server/uidist
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
