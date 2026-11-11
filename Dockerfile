# syntax=docker/dockerfile:1
ARG GO_VERSION=1.27.3

FROM --platform=$BUILDPLATFORM oven/bun:1.3.14-slim AS web-build
WORKDIR /src/web
COPY web/package.json web/bun.lock ./
COPY web/patches ./patches
RUN bun install --frozen-lockfile
COPY web/ ./
COPY api/openapi.json /src/api/openapi.json
COPY internal/locales/*.json /src/internal/locales/
RUN bun run build

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-trixie AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web-build /src/web/dist ./web/dist
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG VCS_REF=unknown
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w -X github.com/Seaony/Kakatta/internal/buildinfo.Version=${VERSION} -X github.com/Seaony/Kakatta/internal/buildinfo.Commit=${VCS_REF}" -o /out/kakatta ./cmd/kakatta \
    && CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w -X github.com/Seaony/Kakatta/internal/buildinfo.Version=${VERSION} -X github.com/Seaony/Kakatta/internal/buildinfo.Commit=${VCS_REF}" -o /out/experiment ./cmd/experiment

FROM debian:trixie-slim AS runtime
ARG TARGETARCH
RUN export DEBIAN_FRONTEND=noninteractive \
    && case "$TARGETARCH" in amd64|arm64) ;; *) exit 1 ;; esac \
    && apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl gnupg gosu tini tzdata xvfb x11vnc fonts-wqy-microhei \
    && curl -fsSL https://dl.google.com/linux/linux_signing_key.pub -o /tmp/google-signing-key.pub \
    && gpg --dearmor -o /usr/share/keyrings/google-chrome.gpg /tmp/google-signing-key.pub \
    && printf 'deb [arch=%s signed-by=/usr/share/keyrings/google-chrome.gpg] https://dl.google.com/linux/chrome/deb/ stable main\n' "$TARGETARCH" > /etc/apt/sources.list.d/google-chrome.list \
    && apt-get update \
    && apt-get install -y --no-install-recommends google-chrome-stable \
    && find /opt/google/chrome/locales -type f ! -name zh-CN.pak ! -name en-US.pak ! -name en-GB.pak -delete \
    && apt-get purge -y gnupg \
    && apt-get autoremove -y \
    && rm -rf /var/lib/apt/lists/* /tmp/google-signing-key.pub \
    && groupadd --gid 1000 kakatta \
    && useradd --uid 1000 --gid kakatta --create-home --shell /usr/sbin/nologin kakatta \
    && install -d -o kakatta -g kakatta -m 0700 /data \
    && install -d -o root -g root -m 1777 /tmp/.X11-unix
ENV TZ=Asia/Shanghai \
    HOME=/data/home XDG_CONFIG_HOME=/data/home/.config LANG=C.UTF-8
COPY --chmod=0755 deploy/entrypoint.sh /usr/local/bin/kakatta-entrypoint
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=15s --retries=3 CMD curl --noproxy '*' -fsS http://127.0.0.1:8080/healthz || exit 1
WORKDIR /data
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/kakatta-entrypoint"]

FROM build AS formal-test-build
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go test -c -trimpath -ldflags="-s -w -X github.com/Seaony/Kakatta/internal/buildinfo.Version=${VERSION} -X github.com/Seaony/Kakatta/internal/buildinfo.Commit=${VCS_REF}" -o /out/formal-path.test ./cmd/kakatta

FROM runtime AS formal-test
COPY --from=build /out/kakatta /usr/local/bin/kakatta
COPY --from=build /out/experiment /usr/local/bin/experiment
COPY --from=formal-test-build /out/formal-path.test /tests/formal-path.test

FROM --platform=$TARGETPLATFORM golang:${GO_VERSION}-trixie AS dev-tools
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
RUN CGO_ENABLED=0 GOBIN=/out go install github.com/air-verse/air@v1.67.4
COPY . .
RUN CGO_ENABLED=0 go build -tags kakatta_dev -buildvcs=false -trimpath -o /out/devhost ./cmd/devhost

FROM runtime AS dev
COPY --from=dev-tools /usr/local/go /usr/local/go
COPY --from=dev-tools /out/air /usr/local/bin/air
COPY --from=dev-tools /out/devhost /usr/local/bin/devhost
COPY --from=dev-tools /go/pkg/mod /go/pkg/mod
COPY --from=dev-tools /root/.cache/go-build /go/build-cache
COPY --chmod=0755 deploy/dev-entrypoint.sh /usr/local/bin/kakatta-dev-entrypoint
ENV PATH="/usr/local/go/bin:${PATH}" GOROOT=/usr/local/go GOPATH=/go \
    GOMODCACHE=/go/pkg/mod GOCACHE=/go/build-cache CGO_ENABLED=0 GOTOOLCHAIN=local
WORKDIR /workspace
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/kakatta-dev-entrypoint"]
CMD ["/usr/local/bin/devhost", "--mode", "real", "--root", "/workspace", "--state", "/data/.development", "--data", "/data", "--listen", "0.0.0.0:18081", "--backend-listen", "127.0.0.1:8080", "--proxy-listen", "0.0.0.0:18080", "--air", "/usr/local/bin/air", "--poll"]

FROM runtime AS final
COPY --from=build /out/kakatta /usr/local/bin/kakatta
COPY --from=build /out/experiment /usr/local/bin/experiment
CMD ["--data", "/data", "--listen", ":8080"]
