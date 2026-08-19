FROM node:24-alpine3.23 AS web-build

WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26.6-alpine3.23 AS go-build

ARG APP_VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web-build /src/web/dist/ ./web/dist/
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w -X main.version=${APP_VERSION}" \
    -o /out/outlook-mail-manager ./cmd/server

FROM alpine:3.23

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 10001 app \
    && adduser -S -D -H -u 10001 -G app app \
    && mkdir -p /data \
    && chown app:app /data

COPY --from=go-build --chown=app:app /out/outlook-mail-manager /usr/local/bin/outlook-mail-manager

USER app
EXPOSE 8080
VOLUME ["/data"]

HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/usr/local/bin/outlook-mail-manager"]
