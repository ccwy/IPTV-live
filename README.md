# IPTV Live

基于 Node.js 代理 + hls.js 的 IPTV 网页播放器，打开即看，无需登录。

## 功能

- 解析并播放 M3U/M3U8 播放列表（默认加载 [iptv-org](https://github.com/iptv-org/iptv) 频道列表）
- 内置 CORS 代理服务器，解决浏览器跨域限制
- 自动重写 M3U8 中的媒体段、子播放列表、密钥 URI，全部走代理
- 支持 Range 请求透传（视频 seek）
- 频道搜索、分组筛选、频道 logo 显示
- 支持自定义播放列表 URL 和本地文件上传
- 记住上次使用的播放列表 URL（localStorage）
- 深色主题，响应式布局，移动端适配

## 快速开始

```bash
node js/server.js
```

浏览器访问 http://localhost:8883 即可观看。

自定义端口：

```bash
set PORT=3000 && node js/server.js
```

## Docker

### 本地构建运行

```bash
docker build -t iptv-live -f docker/Dockerfile .
docker run -d -p 8883:8080 iptv-live
```

### 从 Release 下载镜像运行

1. 进入仓库 **Releases** 页面，按设备架构下载对应 tar 文件（如 `iptv-live-amd64-v1.0.0.tar` / `iptv-live-arm64-v1.0.0.tar`，无需登录 GitHub）
2. 加载并运行：

```bash
docker load -i iptv-live-amd64-v1.0.0.tar
docker run -d -p 8883:8080 iptv-live:latest
```

启动后访问 http://localhost:8080 即可观看。

自定义端口（容器内 `PORT` 环境变量）：

```bash
docker run -d -p 3000:3000 -e PORT=3000 iptv-live:latest
```

> 推送 `v*` 标签，或 push 到 main/master 且 commit message 含 `v*.*.*` 格式版本号时，GitHub Actions 会自动构建 `linux/amd64` + `linux/arm64` 镜像并发布到 GitHub Release（不推送任何镜像仓库）；普通 push 不含版本号时仅构建不发布。

### 群晖 Compose 部署

适用于群晖 NAS（DSM 7.2+，Container Manager）用户，通过「项目」功能以 Compose 方式部署。

**前置：导入镜像**

先从仓库 **Releases** 页面下载对应架构的 tar 文件，再通过以下任一方式导入镜像：

- **图形界面**：Container Manager → 映像 → 导入 → 选择下载的 tar 文件
- **SSH 命令行**：`docker load -i iptv-live-amd64-v1.0.0.tar`

**方式一：Container Manager 图形界面**

1. 打开 Container Manager → 项目 → 新增
2. 填写项目名称（如 `iptv-live`），路径保持默认
3. 在「创建」步骤选择「粘贴 docker-compose.yml 内容」，粘贴下方完整内容
4. 点击「下一步」→「完成」，容器将自动创建并启动

**方式二：SSH 命令行**

将 [docker-compose.yml](docker/docker-compose.yml) 上传到 NAS 任意目录（如 `/volume1/docker/iptv-live/`），在该目录执行：

```bash
docker compose up -d
```

**docker-compose.yml 完整内容**

```yaml
version: "3.8"

services:
  iptv-live:
    image: iptv-live:latest          # 本地导入的镜像名，无需从远程仓库拉取
    container_name: iptv-live
    ports:
      - "8883:8080"                  # 左侧为 NAS 访问端口，可按需修改
    environment:
      # 内置播放列表（JSON 数组，每项含 name 和 url）
      PLAYLISTS: '[{"name":"IPTV-Global","url":"https://iptv-org.github.io/iptv/index.m3u"},{"name":"CCTV","url":"https://example.com/cctv.m3u"},{"name":"港澳台","url":"https://example.com/hk-mo-tw.m3u"}]'
      # PORT: "8080"                 # 容器内端口，默认 8080；修改时需同步端口映射右侧
    restart: unless-stopped
    network_mode: bridge             # 网络模式：bridge（桥接），容器通过端口映射对外提供服务
```

**PLAYLISTS 格式说明**

`PLAYLISTS` 为 JSON 数组格式，每项包含：

- `name`：播放列表显示名称（侧边栏按钮文字）
- `url`：M3U/M3U8 播放列表地址

注意事项：

- YAML 中 JSON 字符串需用**单引号**包裹，内部双引号无需转义
- 未设置 `PLAYLISTS` 时自动加载默认播放列表
- 格式非法时不会导致服务崩溃，仅在日志输出警告并忽略
- 启动后访问 `http://<NAS-IP>:8883` 即可观看

### 配置内置播放列表

通过 `PLAYLISTS` 环境变量可预置多个播放列表，页面侧边栏会显示「内置播放列表」按钮区，点击即可快速切换。

`PLAYLISTS` 为 JSON 数组格式，每项包含 `name`（显示名称）和 `url`（M3U 地址）：

```bash
docker run -d -p 8883:8080 \
  -e PLAYLISTS='[{"name":"IPTV-Global","url":"https://iptv-org.github.io/iptv/index.m3u"},{"name":"CCTV","url":"https://example.com/cctv.m3u"},{"name":"港澳台","url":"https://example.com/hk-mo-tw.m3u"}]' \
  iptv-live:latest
```

Windows 命令行下引号转义较繁琐，推荐使用 `--env-file`。创建 `playlist.env` 文件：

```ini
PLAYLISTS=[{"name":"CCTV","url":"https://example.com/cctv.m3u"},{"name":"港澳台","url":"https://example.com/hk-mo-tw.m3u"}]
```

```bash
docker run -d -p 8080:8080 --env-file playlist.env iptv-live:latest
```

说明：

- 未设置 `PLAYLISTS` 时保持默认行为，自动加载默认播放列表
- `PLAYLISTS` 格式非法时不会导致服务崩溃，仅在日志输出警告并忽略
- 内置列表的第一项作为默认加载项；用户上次使用的 URL 仍会被记住（localStorage）

## 使用方法

1. 页面打开后自动加载默认播放列表
2. 左侧侧边栏可输入其他 M3U 播放列表 URL，点击「加载」
3. 也可点击「上传本地播放列表」选择 `.m3u` / `.m3u8` 文件
4. 使用搜索框按频道名过滤，下拉框按分组筛选
5. 点击频道即可播放

## 技术栈

- **后端**：Node.js 内置模块（http / https / url / fs / path），零外部依赖
- **前端**：单文件 HTML + 内联 CSS/JS，[hls.js](https://github.com/video-dev/hls.js/)（本地引入）
- **代理**：`/proxy?url=<encodeURIComponent(目标URL)>` 端点，流式转发 + M3U8 内容重写

## 项目结构

```
├── js/
│   ├── server.js    # 代理服务器（CORS代理 + M3U8重写 + 静态文件服务）
│   └── hls.min.js   # 本地 hls.js 库
├── html/
│   └── player.html  # 前端播放页面（单文件）
├── img/
│   └── favicon.ico  # 网站图标
├── docker/
│   ├── Dockerfile   # Docker 镜像构建文件
│   └── docker-compose.yml  # 群晖 Docker Compose 部署配置
├── .github/workflows/
│   └── docker-build.yml  # CI 自动构建多平台镜像并发布到 GitHub Release
├── .dockerignore
├── README.md
└── LICENSE
```