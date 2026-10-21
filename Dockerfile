# The Hub as one container: the React UI is built first, then embedded into the
# Go binary (frontend/embed.go), so the final image carries a single executable,
# the recipe catalogue and nothing else.
#
# Build:  docker build -t gospark-hub .
# Run:    docker run -d --name gospark-hub -p 9106:9106 \
#           -v gospark-data:/data --env-file hub.env gospark-hub
#
# The data volume holds the SQLite database, agent files, logs and the vendored
# recipe clones; the catalogue baked into the image is the starting point.

FROM node:24-alpine AS frontend
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
# NODE_ENV=production in the environment trims devDependencies, and vite is one.
RUN env -u NODE_ENV npm run build && test -f dist/index.html

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /src/frontend/dist ./frontend/dist
# CGO off: the SQLite driver is modernc.org/sqlite, pure Go.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags '-s -w' -o /out/gospark-sahd ./cmd/sahd

FROM alpine:3.21
# git: the registry manager clones recipe sources with the git CLI.
# docker-cli is deliberately absent — mount the socket and the CLI in only if
# this container is meant to drive containers; see the run note below.
RUN apk add --no-cache ca-certificates tzdata git \
 && adduser -D -h /app hub

COPY --from=build /out/gospark-sahd /usr/local/bin/gospark-sahd
COPY registry /app/registry

ENV BASE_DIR=/app \
    REGISTRY_PATH=/app/registry/recipes \
    REGISTRY_HOME=/data/registry \
    ENGINE_HOME=/data/engines \
    DATA_DIR=/data \
    DB_PATH=/data/spark-ai-hub.db \
    HOST=0.0.0.0 \
    PORT=9106 \
    PUBLIC_PORT=9106 \
    PROXY_ENABLED=false \
    HOME=/data

RUN mkdir -p /data && chown hub:hub /data
USER hub
VOLUME ["/data"]
EXPOSE 9106

# The SPA index answers without a session; that is enough to know the daemon
# is up and serving its embedded frontend.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
  CMD wget -qO- http://127.0.0.1:9106/ > /dev/null || exit 1

ENTRYPOINT ["/usr/local/bin/gospark-sahd"]
