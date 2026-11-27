# IPTV Live

基于 Go 代理 + hls.js 的 IPTV 网页播放器，打开即看，无需登录。Docker 一键部署。

## 功能

- 解析并播放 M3U/M3U8 播放列表（内置列表通过 `PLAYLISTS` 环境变量配置，未设置则列表为空）
- 内置 CORS 代理服务器，解决浏览器跨域限制
- 自动重写 M3U8 中的媒体段、子播放列表、密钥 URI，全部走代理
- 支持 Range 请求透传（视频 seek）
- 频道搜索、分组筛选、频道 logo 显示
- 支持自定义播放列表 URL 和本地文件上传
- 记住上次使用的播放列表 URL（localStorage）
- 多播放列表支持：侧边栏显示当前播放列表名称，频道计数区实时体现（列表名 · 共 X / Y 个频道）
- 自建直播控制条：播放/暂停按钮、静音/音量调节、LIVE 徽章、画面比例切换（适应/铺满/拉伸）、全屏，鼠标移出自动隐藏
- 代理增强：自动跟随重定向（最多5次）、转发 Referer/Cookie 防盗链、支持自签名 HTTPS 证书、60秒超时、非 UTF-8 编码容错
- 上游 User-Agent 可配置：默认使用 APTV 播放器真实 UA（`AptvPlayer-UA`，部分直播源仅放行 APTV），可通过环境变量 `UPSTREAM_UA` 覆盖（如源要求 `okhttp`/`OTT`/`iPhone` 等）
- 深色主题，响应式布局，移动端适配（侧边栏覆盖式布局、小屏自动收起）

## 快速开始

默认端口 19890，启动后浏览器访问 http://localhost:19890 即可观看。

本项目通过 Docker 部署，见下方 [Docker](#docker) 章节。

## Docker

### 本地构建运行

```bash
docker build -t iptv-live -f docker/Dockerfile .
docker run -d -p 8883:19890 iptv-live
```

### 从 Release 下载镜像运行

1. 进入仓库 **Releases** 页面，按设备架构下载对应 tar 文件（如 `iptv-live-amd64-v1.0.0.tar` / `iptv-live-arm64-v1.0.0.tar`，无需登录 GitHub）
2. 加载并运行：

```bash
docker load -i iptv-live-amd64-v1.0.0.tar
docker run -d -p 8883:19890 iptv-live:latest
```

启动后访问 http://localhost:19890 即可观看。

自定义端口（容器内 `PORT` 环境变量）：

```bash
docker run -d -p 3000:3000 -e PORT=3000 iptv-live:latest
```

> 推送 `v*` 标签，或 push 到 main/master 且 commit message 含 `v*.*.*` 格式版本号时，GitHub Actions 会自动构建 `linux/amd64` + `linux/arm64` Docker 镜像并发布到 GitHub Release（不推送任何镜像仓库）；普通 push 不含版本号时仅构建不发布。

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
    network_mode: host               # 默认 host：容器与宿主机共用网络，直接监听宿主端口 19890
    # host 模式下端口映射不生效；如需改端口用下方 PORT，改回 bridge 时取消 ports 注释并改 network_mode
    # ports:
    #   - "8883:19890"
    environment:
      # 内置播放列表（JSON 数组，每项含 name 和 url）
      PLAYLISTS: '[{"name":"IPTV-Global","url":"https://iptv-org.github.io/iptv/index.m3u"},{"name":"CCTV","url":"https://example.com/cctv.m3u"},{"name":"港澳台","url":"https://example.com/hk-mo-tw.m3u"}]'
      # PORT: "19890"                 # 监听端口（host 模式即宿主端口），默认 19890
      # 放行内网/保留地址（关闭 SSRF 防护），默认 false；代理同网段 NAS 直播源时设为 true
      # ALLOW_PRIVATE_NETWORK: "true"
      # 上游 TLS 证书校验，默认 true 跳过校验（兼容自签名源）；设 false 开启严格校验
      # UPSTREAM_TLS_SKIP_VERIFY: "true"
      # 上游 User-Agent，默认 APTV 真实 UA（AptvPlayer-UA）；如需伪装其它播放器可覆盖
      # UPSTREAM_UA: "AptvPlayer-UA"
    restart: unless-stopped
```

**PLAYLISTS 格式说明**

`PLAYLISTS` 为 JSON 数组格式，每项包含：

- `name`：播放列表显示名称（侧边栏按钮文字）
- `url`：M3U/M3U8 播放列表地址

注意事项：

- YAML 中 JSON 字符串需用**单引号**包裹，内部双引号无需转义
- `PLAYLISTS` 控制侧边栏内置列表按钮，未设置则内置列表为空
- 格式非法时不会导致服务崩溃，仅在日志输出警告并忽略
- 启动后访问 `http://<NAS-IP>:19890` 即可观看（host 模式直接使用宿主端口；若设置了 `PORT` 则用对应端口）

### 配置内置播放列表

通过 `PLAYLISTS` 环境变量可预置多个播放列表，页面侧边栏会显示「内置播放列表」按钮区，点击即可快速切换。

`PLAYLISTS` 为 JSON 数组格式，每项包含 `name`（显示名称）和 `url`（M3U 地址）：

```bash
docker run -d -p 8883:19890 \
  -e PLAYLISTS='[{"name":"IPTV-Global","url":"https://iptv-org.github.io/iptv/index.m3u"},{"name":"CCTV","url":"https://example.com/cctv.m3u"},{"name":"港澳台","url":"https://example.com/hk-mo-tw.m3u"}]' \
  iptv-live:latest
```

Windows 命令行下引号转义较繁琐，推荐使用 `--env-file`。创建 `playlist.env` 文件：

```ini
PLAYLISTS=[{"name":"CCTV","url":"https://example.com/cctv.m3u"},{"name":"港澳台","url":"https://example.com/hk-mo-tw.m3u"}]
```

```bash
docker run -d -p 19890:19890 --env-file playlist.env iptv-live:latest
```

说明：

- `PLAYLISTS` 控制侧边栏内置列表按钮，未设置则内置列表为空
- `PLAYLISTS` 格式非法时不会导致服务崩溃，仅在日志输出警告并忽略
- 加载优先级：上次使用的 URL（localStorage） > 内置列表第一项；都未设置则输入框留空，手动输入即可

## 环境变量

所有可用环境变量（留空/未设置时使用默认值）：

| 变量 | 说明 | 默认值 | 示例 |
|---|---|---|---|
| `PLAYLISTS` | 内置播放列表（JSON 数组，每项含 `name`/`url`） | 空（内置列表为空） | `-e PLAYLISTS='[{"name":"CCTV","url":"https://example.com/cctv.m3u"}]'` |
| `PORT` | 监听端口（host 模式下即宿主机端口） | `19890` | `-e PORT=3000` |
| `UPSTREAM_UA` | 上游请求的 User-Agent；留空则用 APTV 真实 UA | `AptvPlayer-UA` | `-e UPSTREAM_UA="iPhone"` |
| `ALLOW_PRIVATE_NETWORK` | 是否放行内网/保留地址（关闭 SSRF 防护） | `false` | `-e ALLOW_PRIVATE_NETWORK=true` |
| `UPSTREAM_TLS_SKIP_VERIFY` | 是否跳过上游 TLS 证书校验（兼容自签名源） | `true` | `-e UPSTREAM_TLS_SKIP_VERIFY=false` |

> 说明：`UPSTREAM_UA` 不设置时默认使用 APTV 播放器真实 UA `AptvPlayer-UA`（部分直播源仅放行 APTV）；若某些源要求 `okhttp`/`OTT`/`iPhone` 等，用该变量覆盖即可。

## 使用方法

1. 页面打开后自动加载上次使用的播放列表或内置列表第一项（若有）；否则输入框留空，手动输入 M3U 地址
2. 左侧侧边栏可输入其他 M3U 播放列表 URL，点击「加载」
3. 也可点击「上传本地播放列表」选择 `.m3u` / `.m3u8` 文件
4. 使用搜索框按频道名过滤，下拉框按分组筛选
5. 点击频道即可播放

## 技术栈

- **后端**：Go 标准库（net/http / net/url / crypto/tls / embed），零外部依赖，经 go:embed 将静态资源编译进二进制，Docker 镜像基于 scratch 最小化
- **前端**：单文件 HTML + 内联 CSS/JS，[hls.js](https://github.com/video-dev/hls.js/)（本地引入）；自建直播控制条（播放/暂停、音量、画面比例、全屏），无第三方 UI 库
- **代理**：`/proxy?url=<encodeURIComponent(目标URL)>` 端点，流式转发 + M3U8 内容重写
  - 自动跟随 301/302/303/307/308 重定向（最多 5 次跳转，支持相对路径 Location）
  - 转发 Referer/Cookie 头，本站地址自动改写为目标同域，规避防盗链 403
  - HTTPS 放宽证书校验（`tls.Config{InsecureSkipVerify: true}`），兼容自签名证书源
  - 连接超时 60 秒，慢速源友好
  - 非 UTF-8 编码的 M3U8 检测到乱码时放弃重写、透传原始内容
- **静态资源**：经 go:embed 编译进二进制，运行时无需外部文件

## 项目结构

```
├── main.go          # 入口：配置加载、路由、静态文件服务
├── proxy.go         # CORS 代理：重定向跟随、M3U8 检测、流式转发
├── rewrite.go       # M3U8 内容重写、URL 解析、encodeURIComponent
├── embed.go         # go:embed 静态资源嵌入
├── go.mod           # Go 模块定义（零外部依赖）
├── html/
│   └── player.html  # 前端播放页面（单文件）
├── js/
│   └── hls.min.js   # 本地 hls.js 库（浏览器端播放）
├── img/
│   └── favicon.ico  # 网站图标
├── docker/
│   ├── Dockerfile   # Go 多阶段构建（scratch 最小镜像）
│   └── docker-compose.yml  # 群晖 Docker Compose 部署配置
├── .github/workflows/
│   └── docker-build.yml  # CI：Docker 镜像构建发布
├── .dockerignore
├── README.md
└── LICENSE
```
