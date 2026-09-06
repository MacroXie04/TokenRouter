# syntax=docker/dockerfile:1
# TokenRouter multi-stage build: frontend (npm) -> backend (go) -> runtime.

FROM node:22-bookworm-slim AS frontend
WORKDIR /build/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web ./
RUN npm run build

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
