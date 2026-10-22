# IPTV Live - Node.js 零依赖代理服务器
FROM node:22-alpine

# 设置工作目录
WORKDIR /app

# 复制运行必需目录（无需 npm install，零外部依赖）
COPY js/ ./js/
COPY html/ ./html/
COPY img/ ./img/

# 内置播放列表（JSON数组，空默认值，运行时通过 -e PLAYLISTS='[...]' 覆盖）
ENV PLAYLISTS=

# 暴露端口（支持 PORT 环境变量覆盖，默认 8080）
EXPOSE 8080

# 健康检查（alpine 无 curl，使用 wget）
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget --no-verbose --tries=1 --spider http://localhost:8080/ || exit 1

# 启动服务器
CMD ["node", "js/server.js"]