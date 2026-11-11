# 两个目标：
#   runtime（默认，最后一个阶段）：从源码构建，供开发和 CI 使用；
#   release：不编译，直接复制构建上下文 dist/ 里的发布包内容（cdk-recharge、web、VERSION），
#            发版时用它，镜像里的程序与在线升级下载的发布包是同一份文件。
#   docker build --target release -t xxx .     （需先把发布包解压到 ./dist）

# ===== 构建阶段 1：前端 =====
FROM node:25-alpine AS frontend
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY frontend/ ./
RUN npm run build

# ===== 构建阶段 2：后端（CGO，SQLite 需要） =====
FROM golang:1.27-trixie AS backend
# 版本号：构建参数 VERSION 优先，留空则读仓库的 VERSION 文件
ARG VERSION=""
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY VERSION /src/VERSION
COPY backend/ ./
RUN set -eu; \
    ver="${VERSION:-$(cat /src/VERSION)}"; ver="$(printf '%s' "${ver#v}" | tr -d ' \r\n')"; \
    mkdir -p /out; printf '%s\n' "$ver" > /out/VERSION; \
    CGO_ENABLED=1 GOOS=linux go build -trimpath \
      -ldflags="-s -w -X github.com/tuzi/cdk-recharge-system/internal/handler.BuildVersion=${ver}" \
      -o /out/cdk-recharge ./cmd/server

# ===== 运行环境（两个目标共用） =====
FROM debian:trixie-slim AS runtime-base
LABEL org.opencontainers.image.source=https://github.com/hongheshan-svg/aisubproxy
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
       chromium xvfb fonts-liberation fonts-noto-cjk ca-certificates tzdata curl \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --no-create-home --shell /usr/sbin/nologin cdk-chrome \
    && mkdir -p /app/data
WORKDIR /app

# TRUSTED_PROXIES 默认值（A-18/F-8）：127.0.0.1 加 172.16.0.0/12，给 docker-compose.yml 里的 Caddy 用（compose 会覆盖为 172.16.0.0/12）。
# 直接 `docker run -p 8080:8080` 发布端口时必须用 -e TRUSTED_PROXIES=127.0.0.1（或置空）覆盖：
# 否则 Docker userland-proxy 把来源改写成网桥网关，落在可信段内，X-Forwarded-For 可伪造，限流和审计 IP 失效。
# 这里不直接改默认值，是因为容器里的反代（不在 compose 里）依赖它。
# 程序以 root 运行，启动 Chrome 时降权到 cdk-chrome 用户并保留沙箱
ENV DB_PATH=/app/data/cdk_recharge.db \
    WEB_DIR=/app/web \
    SERVER_HOST=0.0.0.0 \
    SERVER_PORT=8080 \
    SERVER_MODE=release \
    INSTALL_MODE=wizard \
    TRUSTED_PROXIES=127.0.0.1,172.16.0.0/12 \
    CDK_DEPLOY_MODE=docker \
    CHROME_PATH=/usr/bin/chromium

VOLUME ["/app/data"]
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD curl -f http://127.0.0.1:8080/health || exit 1

CMD ["/app/cdk-recharge"]

# ===== 目标 release：复制发布包内容 =====
FROM runtime-base AS release
COPY dist/cdk-recharge /app/cdk-recharge
COPY dist/web /app/web
COPY dist/VERSION /app/VERSION

# ===== 目标 runtime（默认，必须是最后一个阶段）：从源码构建 =====
FROM runtime-base AS runtime
COPY --from=backend  /out/cdk-recharge /app/cdk-recharge
COPY --from=backend  /out/VERSION /app/VERSION
COPY --from=frontend /src/frontend/dist /app/web
