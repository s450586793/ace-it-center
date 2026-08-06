# syntax=docker/dockerfile:1.7

ARG GO_VERSION=1.26.0
ARG NODE_VERSION=24-alpine
ARG GOPROXY=https://goproxy.cn,direct
ARG NPM_REGISTRY=https://registry.npmmirror.com

FROM golang:${GO_VERSION}-alpine AS go-base
ARG GOPROXY
ENV GOPROXY=${GOPROXY}
WORKDIR /src
RUN apk add --no-cache ca-certificates git
COPY go.mod go.sum ./
RUN go mod download

FROM go-base AS backend-builder
COPY backend ./backend
COPY internal ./internal
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/ace-it-center ./backend/cmd/server

FROM go-base AS updater-builder
COPY updater ./updater
COPY internal ./internal
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/ace-updater ./updater/cmd/ace-updater

FROM go-base AS agent-builder
COPY agent ./agent
COPY internal ./internal
RUN mkdir -p /out \
    && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
       go build -trimpath -ldflags="-s -w" -o /out/ace-agent-linux-amd64 ./agent/cmd/ace-agent \
    && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
       go build -trimpath -ldflags="-s -w" -o /out/AceAgent-windows-amd64.exe ./agent/cmd/ace-agent

FROM node:${NODE_VERSION} AS frontend-builder
ARG NPM_REGISTRY
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci --registry=${NPM_REGISTRY}
COPY frontend ./
RUN npm run build

FROM alpine:3.22 AS backend
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S ace \
    && adduser -S -G ace ace
COPY --from=backend-builder /out/ace-it-center /usr/local/bin/ace-it-center
USER ace
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/ace-it-center"]

FROM nginx:1.29-alpine AS web
COPY deploy/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=frontend-builder /src/frontend/dist /usr/share/nginx/html
COPY --from=agent-builder /out /usr/share/nginx/html/downloads
EXPOSE 80

FROM alpine:3.22 AS updater
RUN apk add --no-cache \
    ca-certificates \
    tzdata \
    docker-cli \
    docker-cli-compose \
    postgresql16-client
COPY --from=updater-builder /out/ace-updater /usr/local/bin/ace-updater
EXPOSE 8090
ENTRYPOINT ["/usr/local/bin/ace-updater"]
