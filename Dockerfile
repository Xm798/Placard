# syntax=docker/dockerfile:1

# Both build stages are pinned to the BUILD platform: the frontend bundle is
# platform-independent and the server is built with CGO off, so a multi-arch
# image cross-compiles instead of emulating the target under QEMU.

# ---- web build stage ----
FROM --platform=$BUILDPLATFORM node:22-slim AS webbuild
# Mirror the repo layout so vite's outDir (../internal/web/dist, resolved from
# the frontend/ dir) lands at /src/internal/web/dist — the exact path the Go
# stage embeds via //go:embed all:dist.
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
# `npm ci`, not `npm install`: the build stays pinned to the committed,
# integrity-checked lock.
RUN npm ci --no-audit --no-fund
COPY frontend/ ./
RUN npm run build

# ---- build stage ----
FROM --platform=$BUILDPLATFORM golang:1.26 AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Overlay the built frontend AFTER `COPY . .` (which only carries dist/.gitkeep)
# and BEFORE `go build`, so //go:embed all:dist sees the real index.html + assets.
COPY --from=webbuild /src/internal/web/dist /src/internal/web/dist

# Build metadata is injected via ldflags. release-server.yml passes it; a hand
# rolled build that omits it reports version "dev":
#   docker build \
#     --build-arg VERSION=$(git describe --tags --match 'v[0-9]*' --always --dirty) \
#     --build-arg GIT_COMMIT=$(git rev-parse --short HEAD) \
#     --build-arg GIT_BRANCH=$(git branch --show-current) \
#     --build-arg BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ) \
#     -t placard .
# --match 'v[0-9]*' skips the CLI's cli/vX.Y.Z tags.
ARG VERSION=dev
ARG BUILD_TIME=unknown
ARG GIT_COMMIT=unknown
ARG GIT_BRANCH=unknown
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w \
    -X github.com/Xm798/placard/internal/version.Version=${VERSION} \
    -X github.com/Xm798/placard/internal/version.BuildTime=${BUILD_TIME} \
    -X github.com/Xm798/placard/internal/version.GitCommit=${GIT_COMMIT} \
    -X github.com/Xm798/placard/internal/version.GitBranch=${GIT_BRANCH}" \
    -o /out/placard-server ./

# The runtime image has no shell to mkdir with, so the data directory is staged
# here. 65532 is distroless's nonroot uid; Docker copies this ownership onto a
# fresh volume mounted over /data, which is what lets the server write there.
RUN mkdir -p /out/data

# ---- runtime stage ----
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/placard-server /usr/local/bin/placard-server
COPY --from=build --chown=65532:65532 /out/data /data

# Everything the instance writes lives in the volume: the SQLite database,
# objects and secret.key under data_dir, and the log file at the default
# relative ./logs/placard.log. Running from /data also means a config.yaml
# dropped into the volume is found with no flag or env var.
WORKDIR /data
ENV PLACARD_DATA_DIR=/data
VOLUME /data

EXPOSE 8080

# Exec form: the runtime image has no shell. Liveness only — see the
# healthcheck package for why readiness is not probed.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/usr/local/bin/placard-server", "healthcheck"]

ENTRYPOINT ["/usr/local/bin/placard-server"]
