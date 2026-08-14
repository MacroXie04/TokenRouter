# syntax=docker/dockerfile:1
# TokenRouter multi-stage build: frontend (bun) -> backend (go) -> runtime.

FROM oven/bun:1 AS frontend
WORKDIR /build/web
COPY web/package.json web/bun.lock* ./
RUN bun install --frozen-lockfile || bun install
COPY web ./
RUN bun run build

FROM golang:1.26-alpine AS backend
ENV GO111MODULE=on CGO_ENABLED=0 GOWORK=off
WORKDIR /build
COPY go.mod go.sum ./
COPY protocolkit/go.mod ./protocolkit/go.mod
RUN go mod download
COPY . .
COPY --from=frontend /build/web/dist ./web/dist
ARG VERSION=dev
RUN go build -ldflags "-s -w -X 'github.com/tokenrouter/tokenrouter/common.Version=${VERSION}'" -o tokenrouter .

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata wget \
    && rm -rf /var/lib/apt/lists/* \
    && update-ca-certificates
COPY --from=backend /build/tokenrouter /tokenrouter
COPY LICENSE NOTICE THIRD-PARTY-LICENSES.md /licenses/
EXPOSE 3000
WORKDIR /data
ENTRYPOINT ["/tokenrouter"]
