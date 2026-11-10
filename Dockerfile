# syntax=docker/dockerfile:1
ARG GO_VERSION=1.27.3

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-trixie AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags='-s -w' -o /out/experiment ./cmd/experiment

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
ENV PUID=1000 PGID=1000 TZ=Asia/Shanghai \
    HOME=/data/home XDG_CONFIG_HOME=/data/home/.config LANG=C.UTF-8
COPY --chmod=0755 deploy/entrypoint.sh /usr/local/bin/kakatta-entrypoint
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=15s --retries=3 CMD curl --noproxy '*' -fsS http://127.0.0.1:8080/healthz || exit 1
WORKDIR /data
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/kakatta-entrypoint"]

FROM runtime AS final
COPY --from=build /out/experiment /usr/local/bin/experiment
CMD ["--data", "/data", "--listen", ":8080"]
