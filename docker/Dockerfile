# IPTV Live - Node.js 零依赖代理服务器（多阶段构建，最小化镜像体积）
# 阶段一：从官方 node 镜像提取 node 二进制
FROM node:22-alpine AS node-builder

# 阶段二：纯净 alpine + node 二进制，剔除 npm/corepack/yarn
FROM alpine:3.20

# node 二进制依赖的运行时库（musl 静态链接，仅缺 libstdc++/libgcc）
# ffmpeg：用于 HEVC/H.265→H.264 实时转码（浏览器不支持 HEVC 时由服务端转码后播放）
RUN apk add --no-cache libstdc++ libgcc ffmpeg

# 仅拷贝 node 可执行文件，不带 npm/corepack
COPY --from=node-builder /usr/local/bin/node /usr/local/bin/node

WORKDIR /app

# 复制运行必需目录（无需 npm install，零外部依赖）
COPY js/ ./js/
COPY html/ ./html/
COPY img/ ./img/

# 内置播放列表（JSON数组，空默认值，运行时通过 -e PLAYLISTS='[...]' 覆盖）
ENV PLAYLISTS=

# H.265/HEVC 转码开关（镜像内已装 ffmpeg，默认启用；NAS 无硬解性能不足时可设为 false）
ENV ENABLE_TRANSCODE=true

# 暴露端口（支持 PORT 环境变量覆盖，默认 8080）
EXPOSE 8080

# 健康检查（alpine busybox 自带 wget）
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget --no-verbose --tries=1 --spider http://localhost:8080/ || exit 1

# 启动服务器
CMD ["node", "js/server.js"]