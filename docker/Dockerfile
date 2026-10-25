# IPTV Live - Go 零依赖代理服务器（多阶段构建，最小化镜像体积）
# 阶段一：Go 构建（CGO_ENABLED=0 静态编译，产出无依赖单二进制，静态资源经 go:embed 打入）
FROM golang:1.22-alpine AS builder
WORKDIR /build
COPY go.mod ./
COPY *.go ./
COPY html/ ./html/
COPY js/ ./js/
COPY img/ ./img/
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /iptv-live .

# 阶段二：scratch 最小镜像（仅含静态二进制，无 shell/包管理器，约 10-15MB）
FROM scratch
COPY --from=builder /iptv-live /iptv-live

# 内置播放列表（JSON数组，空默认值，运行时通过 -e PLAYLISTS='[...]' 覆盖）
ENV PLAYLISTS=

# 暴露端口（支持 PORT 环境变量覆盖，默认 8080）
EXPOSE 8080

# 健康检查：scratch 基础镜像无 wget/curl/shell，无法容器内自检
# 服务内置 /health 端点返回 200，供外部健康探测使用
# 如需容器内 HEALTHCHECK，可将基础镜像改为 alpine 并用 wget 探测 /health：
# HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
#   CMD wget --no-verbose --tries=1 --spider http://localhost:8080/health || exit 1

# 启动服务器
ENTRYPOINT ["/iptv-live"]