#!/usr/bin/env node
/**
 * IPTV Live 代理服务器（零外部依赖）
 * 功能：CORS代理（含M3U8重写）、静态文件服务
 */

const http = require('http');
const https = require('https');
const fs = require('fs');
const path = require('path');
const { spawn, spawnSync } = require('child_process');
const crypto = require('crypto');
const os = require('os');

const PORT = process.env.PORT || 8080;
const DEFAULT_PLAYLIST = 'https://iptv-org.github.io/iptv/index.m3u';
const ROOT_DIR = path.join(__dirname, '..');

// ===== 转码配置（HEVC/H.265 → H.264 服务端实时转码）=====
// 功能开关：环境变量 ENABLE_TRANSCODE，默认 false；值为 'true'/'1'/'yes' 时启用
const ENABLE_TRANSCODE = ['true', '1', 'yes'].includes(String(process.env.ENABLE_TRANSCODE || 'false').trim().toLowerCase());
// ffmpeg 可用性：启动时同步检测一次
let ffmpegAvailable = false;
try {
  ffmpegAvailable = spawnSync('ffmpeg', ['-version'], { stdio: 'ignore', timeout: 5000 }).status === 0;
} catch (e) {
  ffmpegAvailable = false;
}
// 转码会话管理：<sessionId, { dir, process, lastAccess, url }>
const transcodeSessions = new Map();
const TRANSCODE_BASE_DIR = path.join(os.tmpdir(), 'iptv-transcode');
const TRANSCODE_IDLE_MS = 5 * 60 * 1000; // 5 分钟无访问自动清理

// 内置播放列表（环境变量 PLAYLISTS，JSON数组格式：[{"name":"名称","url":"M3U地址"}]）
// 解析失败时仅输出警告并忽略，不影响服务启动
let BUILTIN_PLAYLISTS = [];
if (process.env.PLAYLISTS) {
  try {
    const parsed = JSON.parse(process.env.PLAYLISTS);
    if (Array.isArray(parsed)) {
      BUILTIN_PLAYLISTS = parsed.filter((item) =>
        item && typeof item.name === 'string' && typeof item.url === 'string'
      );
    } else {
      console.warn('PLAYLISTS 环境变量不是 JSON 数组，已忽略');
    }
  } catch (e) {
    console.warn('PLAYLISTS 环境变量解析失败，已忽略: ' + e.message);
  }
}

const MIME_TYPES = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'application/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.jpeg': 'image/jpeg',
  '.gif': 'image/gif',
  '.svg': 'image/svg+xml',
  '.ico': 'image/x-icon',
  '.m3u': 'application/vnd.apple.mpegurl',
  '.m3u8': 'application/vnd.apple.mpegurl',
  '.ts': 'video/mp2t',
  '.mp4': 'video/mp4',
  '.txt': 'text/plain; charset=utf-8',
};

// 需要从目标响应中移除的头（会干扰CORS或缓存）
const STRIP_HEADERS = new Set([
  'access-control-allow-origin',
  'access-control-allow-methods',
  'access-control-allow-headers',
  'access-control-allow-credentials',
  'access-control-expose-headers',
  'access-control-max-age',
  'content-security-policy',
  'content-security-policy-report-only',
  'x-frame-options',
  'strict-transport-security',
  'content-encoding',
  'transfer-encoding',
  'connection',
  'keep-alive',
]);

// M3U8相关Content-Type
const M3U8_TYPES = [
  'application/vnd.apple.mpegurl',
  'application/x-mpegurl',
  'application/mpegurl',
  'text/plain', // 有些服务器用text/plain返回m3u8，需结合内容判断
];

function isM3U8ContentType(contentType) {
  if (!contentType) return false;
  const ct = contentType.split(';')[0].trim().toLowerCase();
  return M3U8_TYPES.includes(ct) || ct.includes('mpegurl');
}

function looksLikeM3U8(content) {
  if (!content) return false;
  const head = content.slice(0, 512).trim();
  return head.startsWith('#EXTM3U') || head.startsWith('#EXTINF') || head.startsWith('#EXT-X');
}

/**
 * 将m3u8/m3u内容中的URL重写为代理URL
 */
function rewriteM3U8(content, baseUrl) {
  if (!content) return content;
  const lines = content.split(/\r?\n/);

  const rewritten = lines.map((line) => {
    const trimmed = line.trim();
    if (!trimmed) return line;

    // 注释行：处理其中的URI="..."属性
    if (trimmed.startsWith('#')) {
      return rewriteAttributes(trimmed, baseUrl);
    }

    // 非注释非空行 = URL行（媒体段、子播放列表、或m3u频道流URL）
    const absolute = resolveUrl(trimmed, baseUrl);
    if (absolute) {
      return '/proxy?url=' + encodeURIComponent(absolute);
    }
    return line;
  });

  return rewritten.join('\n');
}

/**
 * 重写标签属性中的 URI="..."（如 EXT-X-KEY、EXT-X-MAP、EXT-X-MEDIA、EXT-X-STREAM-INF）
 */
function rewriteAttributes(line, baseUrl) {
  return line.replace(/URI="([^"]+)"/g, (match, uri) => {
    const absolute = resolveUrl(uri, baseUrl);
    if (absolute) {
      return 'URI="/proxy?url=' + encodeURIComponent(absolute) + '"';
    }
    return match;
  });
}

/**
 * 基于baseUrl解析相对URL为绝对URL
 */
function resolveUrl(relative, baseUrl) {
  if (!relative) return null;
  try {
    // 已经是代理URL就不重复处理
    if (relative.startsWith('/proxy?url=')) return null;
    return new URL(relative, baseUrl).href;
  } catch (e) {
    return null;
  }
}

/**
 * 发送JSON响应
 */
function sendJSON(res, statusCode, obj) {
  if (res.headersSent || res.writableEnded) return;
  const body = JSON.stringify(obj);
  try {
    res.writeHead(statusCode, {
      'Content-Type': 'application/json; charset=utf-8',
      'Access-Control-Allow-Origin': '*',
      'Content-Length': Buffer.byteLength(body),
    });
    res.end(body);
  } catch (e) {}
}

/**
 * 设置CORS响应头
 */
function setCorsHeaders(res) {
  res.setHeader('Access-Control-Allow-Origin', '*');
  res.setHeader('Access-Control-Allow-Methods', 'GET, POST, OPTIONS');
  res.setHeader('Access-Control-Allow-Headers', '*');
}

/**
 * 代理请求处理
 */
function handleProxy(req, res) {
  let parsedUrl;
  try {
    parsedUrl = new URL(req.url, 'http://localhost');
  } catch (e) {
    return sendJSON(res, 400, { error: '无效的请求URL' });
  }
  const targetUrl = parsedUrl.searchParams.get('url');

  if (!targetUrl) {
    return sendJSON(res, 400, { error: '缺少 url 参数。用法: /proxy?url=<encodeURIComponent(目标URL)>' });
  }

  let target;
  try {
    target = new URL(targetUrl);
  } catch (e) {
    return sendJSON(res, 400, { error: '无效的URL: ' + targetUrl });
  }

  if (target.protocol !== 'http:' && target.protocol !== 'https:') {
    return sendJSON(res, 400, { error: '仅支持 http/https 协议' });
  }

  // 构造基础转发头（含 referer/cookie），具体请求头在 fetchUpstream 中按目标重建
  const baseHeaders = buildForwardHeaders(req);
  fetchUpstream(req, res, targetUrl, 0, baseHeaders);
}

/**
 * 构造转发请求头：复制必要头（含 referer/cookie），固定使用 APTV 播放器 UA
 */
function buildForwardHeaders(req) {
  const forwardHeaders = {};
  const copyHeaders = ['range', 'accept', 'accept-language', 'referer', 'cookie'];
  for (const h of copyHeaders) {
    if (req.headers[h]) forwardHeaders[h] = req.headers[h];
  }
  // 固定 APTV（Apple TV 播放器）UA：部分直播源仅允许特定播放器 UA 访问
  forwardHeaders['user-agent'] = 'APTV/1.0';
  return forwardHeaders;
}

/**
 * 递归请求上游：跟随 301/302/303/307/308 重定向，最多 5 次
 */
function fetchUpstream(req, res, targetUrl, depth, baseHeaders) {
  if (depth > 5) {
    return sendJSON(res, 502, { error: '重定向次数过多', url: targetUrl });
  }

  let target;
  try {
    target = new URL(targetUrl);
  } catch (e) {
    return sendJSON(res, 400, { error: '无效的重定向URL: ' + targetUrl });
  }
  if (target.protocol !== 'http:' && target.protocol !== 'https:') {
    return sendJSON(res, 400, { error: '重定向目标仅支持 http/https 协议' });
  }

  const lib = target.protocol === 'https:' ? https : http;

  // 每次请求基于当前目标重建头（host/referer 随重定向更新）
  const forwardHeaders = Object.assign({}, baseHeaders);
  forwardHeaders['host'] = target.host;
  // referer 改写：本站地址（localhost/本机IP）改写为目标源同域 referer，避免源服务器校验失败返回 403
  if (forwardHeaders['referer']) {
    try {
      const refHost = new URL(forwardHeaders['referer']).hostname;
      const isLocal = refHost === 'localhost' || refHost === '127.0.0.1' || refHost === '0.0.0.0' || refHost === '::1'
        || /^192\.168\./.test(refHost) || /^10\./.test(refHost) || /^172\.(1[6-9]|2\d|3[01])\./.test(refHost);
      if (isLocal) forwardHeaders['referer'] = target.origin + '/';
    } catch (e) {
      delete forwardHeaders['referer'];
    }
  }
  forwardHeaders['accept-encoding'] = 'identity'; // 避免目标返回压缩内容导致解码失败

  const options = {
    hostname: target.hostname,
    port: target.port || (target.protocol === 'https:' ? 443 : 80),
    path: target.pathname + target.search,
    method: 'GET', // 代理只做GET
    headers: forwardHeaders,
    timeout: 60000, // 慢速源容易超时，延长到 60s
  };
  if (target.protocol === 'https:') {
    options.rejectUnauthorized = false; // 放宽自签名证书校验，部分小众源自签名
  }

  const proxyReq = lib.request(options, (proxyRes) => {
    const contentType = proxyRes.headers['content-type'] || '';
    const statusCode = proxyRes.statusCode || 502;

    // 跟随重定向：读取 Location 头拼接新地址后递归请求，最多 5 次（防死循环）
    if (statusCode === 301 || statusCode === 302 || statusCode === 303
      || statusCode === 307 || statusCode === 308) {
      const location = proxyRes.headers['location'];
      proxyRes.resume(); // 丢弃重定向响应体，不转发 body
      if (!location) {
        return sendJSON(res, 502, { error: '重定向响应缺少 Location 头', url: targetUrl });
      }
      let nextUrl;
      try {
        nextUrl = new URL(location, targetUrl).href; // Location 可能是相对路径
      } catch (e) {
        return sendJSON(res, 502, { error: '无效的重定向地址: ' + location });
      }
      return fetchUpstream(req, res, nextUrl, depth + 1, baseHeaders);
    }

    // 判断是否需要重写m3u8内容（Content-Type或URL后缀）
    const urlPath = target.pathname.toLowerCase();
    const isM3U8 = isM3U8ContentType(contentType)
      || urlPath.endsWith('.m3u8')
      || urlPath.endsWith('.m3u');

    if (isM3U8) {
      // 缓冲m3u8内容进行重写
      const chunks = [];
      proxyRes.on('data', (chunk) => chunks.push(chunk));
      proxyRes.on('end', () => {
        const rawBuf = Buffer.concat(chunks);
        const decoded = rawBuf.toString('utf-8');
        // 编码容错：UTF-8 解码出现替换符 U+FFFD 说明源内容非 UTF-8，
        // 放弃重写直接透传原始 Buffer（不重写比写坏好）
        if (decoded.indexOf(String.fromCodePoint(0xFFFD)) !== -1) {
          setCorsHeaders(res);
          res.setHeader('Content-Type', contentType || 'application/vnd.apple.mpegurl');
          res.setHeader('Content-Length', rawBuf.length);
          res.writeHead(statusCode === 304 ? 200 : statusCode);
          return res.end(rawBuf);
        }
        let body = decoded;
        const isActuallyM3U8 = looksLikeM3U8(body);
        if (isActuallyM3U8) {
          body = rewriteM3U8(body, targetUrl);
        }
        setCorsHeaders(res);
        res.setHeader('Content-Type', isActuallyM3U8
          ? 'application/vnd.apple.mpegurl; charset=utf-8'
          : (contentType || 'text/plain; charset=utf-8'));
        res.setHeader('Content-Length', Buffer.byteLength(body));
        res.writeHead(statusCode === 304 ? 200 : statusCode);
        res.end(body);
      });
      proxyRes.on('error', (err) => {
        sendJSON(res, 502, { error: '目标服务器响应错误: ' + err.message });
      });
    } else {
      // 流式转发（视频段等大文件）
      const headers = filterHeaders(proxyRes.headers);
      setCorsHeaders(res);
      for (const [k, v] of Object.entries(headers)) {
        res.setHeader(k, v);
      }
      res.writeHead(statusCode);
      proxyRes.pipe(res);
      proxyRes.on('error', () => {
        try { res.end(); } catch (e) {}
      });
    }
  });

  // 客户端断开时中止上游请求，避免向已关闭的流写入触发未捕获异常
  res.on('close', () => {
    proxyReq.destroy();
  });

  proxyReq.on('timeout', () => {
    proxyReq.destroy();
    sendJSON(res, 504, { error: '连接目标服务器超时(60s): ' + targetUrl });
  });

  proxyReq.on('error', (err) => {
    sendJSON(res, 502, { error: '无法连接目标服务器: ' + err.message, url: targetUrl });
  });

  proxyReq.end();
}

/**
 * 过滤目标响应头，移除干扰CORS的头
 */
function filterHeaders(headers) {
  const result = {};
  for (const [key, value] of Object.entries(headers)) {
    const lower = key.toLowerCase();
    if (STRIP_HEADERS.has(lower)) continue;
    result[key] = value;
  }
  return result;
}

/**
 * ===== HEVC/H.265 → H.264 服务端实时转码 =====
 * 拉取上游 HLS → ffmpeg 转码为 H.264+AAC → 输出 HLS 分片到临时目录
 * /transcode?url=xxx            创建/复用会话，返回重写分片URL的 m3u8
 * /transcode-seg/<sid>/<file>   读取会话临时目录的分片文件
 * /transcode-status             返回 { enabled, ffmpegAvailable }
 */

/**
 * 清理转码会话：kill ffmpeg 进程并删除临时目录
 */
function cleanupTranscodeSession(sessionId) {
  const session = transcodeSessions.get(sessionId);
  if (!session) return;
  transcodeSessions.delete(sessionId);
  if (session.process) {
    try { session.process.kill('SIGKILL'); } catch (e) {}
    session.process = null;
  }
  try { fs.rmSync(session.dir, { recursive: true, force: true }); } catch (e) {}
}

/**
 * 创建转码会话：spawn ffmpeg 拉流并输出 HLS 分片到临时目录
 */
function createTranscodeSession(targetUrl) {
  const sessionId = crypto.randomBytes(8).toString('hex');
  const sessionDir = path.join(TRANSCODE_BASE_DIR, sessionId);
  fs.mkdirSync(sessionDir, { recursive: true });

  // 构造与代理一致的请求头（APTV UA + 目标同域 Referer），通过 ffmpeg HTTP 输入选项传入
  let refererHeaders = '';
  try {
    refererHeaders = 'Referer: ' + new URL(targetUrl).origin + '/\r\n';
  } catch (e) {}

  const args = ['-hide_banner', '-loglevel', 'error', '-user_agent', 'APTV/1.0'];
  if (refererHeaders) args.push('-headers', refererHeaders);
  args.push(
    '-i', targetUrl,
    '-c:v', 'libx264', '-preset', 'veryfast', '-tune', 'zerolatency',
    '-c:a', 'aac', '-b:a', '128k',
    '-f', 'hls',
    '-hls_time', '2',
    '-hls_list_size', '3',
    '-hls_flags', 'delete_segments+omit_endlist',
    '-hls_segment_filename', path.join(sessionDir, 'seg%05d.ts'),
    path.join(sessionDir, 'index.m3u8')
  );

  const proc = spawn('ffmpeg', args, { stdio: ['ignore', 'ignore', 'pipe'] });
  const session = { dir: sessionDir, process: proc, lastAccess: Date.now(), url: targetUrl };
  transcodeSessions.set(sessionId, session);

  // 消费 stderr 防止缓冲写满阻塞，保留末尾内容用于异常退出时排查
  let stderrTail = '';
  proc.stderr.on('data', (d) => {
    stderrTail = (stderrTail + d.toString()).slice(-512);
  });
  proc.on('error', (err) => {
    console.warn('[转码] 启动 ffmpeg 失败: ' + err.message);
    cleanupTranscodeSession(sessionId);
  });
  proc.on('exit', (code) => {
    if (code !== 0 && code !== null) {
      console.warn('[转码] 会话 ' + sessionId + ' 异常退出(code=' + code + '): ' + stderrTail.trim().split('\n').pop());
    }
    cleanupTranscodeSession(sessionId);
  });
  return { sessionId: sessionId, session: session };
}

/**
 * 重写转码 m3u8：将相对分片文件名替换为 /transcode-seg/<sessionId>/ 端点
 */
function rewriteTranscodeM3U8(content, sessionId) {
  const segPrefix = '/transcode-seg/' + sessionId + '/';
  return content.split(/\r?\n/).map((line) => {
    const trimmed = line.trim();
    if (!trimmed) return line;
    if (trimmed.charAt(0) === '#') {
      // 处理 URI="..." 属性（如 EXT-X-MAP，转码 TS 流通常无此标签，保险起见处理）
      return line.replace(/URI="([^"]+)"/g, (m, uri) => {
        if (/^https?:\/\//i.test(uri) || uri.indexOf('/transcode-seg/') === 0) return m;
        return 'URI="' + segPrefix + uri + '"';
      });
    }
    // 分片行（相对文件名 seg00000.ts）
    if (/^https?:\/\//i.test(trimmed) || trimmed.charAt(0) === '/') return line;
    return segPrefix + trimmed;
  }).join('\n');
}

/**
 * /transcode 端点处理：参数校验复用 handleProxy 风格
 */
function handleTranscode(req, res) {
  if (!ENABLE_TRANSCODE) {
    return sendJSON(res, 403, { error: '转码功能未启用（需设置环境变量 ENABLE_TRANSCODE=true）' });
  }
  if (!ffmpegAvailable) {
    return sendJSON(res, 501, { error: '服务器未安装 ffmpeg，无法转码' });
  }

  let parsedUrl;
  try {
    parsedUrl = new URL(req.url, 'http://localhost');
  } catch (e) {
    return sendJSON(res, 400, { error: '无效的请求URL' });
  }
  const targetUrl = parsedUrl.searchParams.get('url');
  if (!targetUrl) {
    return sendJSON(res, 400, { error: '缺少 url 参数。用法: /transcode?url=<encodeURIComponent(目标URL)>' });
  }
  let target;
  try {
    target = new URL(targetUrl);
  } catch (e) {
    return sendJSON(res, 400, { error: '无效的URL: ' + targetUrl });
  }
  if (target.protocol !== 'http:' && target.protocol !== 'https:') {
    return sendJSON(res, 400, { error: '仅支持 http/https 协议' });
  }

  // 会话复用：同一频道 URL 已有存活会话时直接复用（hls.js 轮询 m3u8 场景，避免重复拉流转码）
  let sessionId = null;
  let session = null;
  for (const [id, s] of transcodeSessions) {
    if (s.url === targetUrl && s.process) {
      sessionId = id;
      session = s;
      break;
    }
  }
  if (!session) {
    const created = createTranscodeSession(targetUrl);
    sessionId = created.sessionId;
    session = created.session;
  }
  session.lastAccess = Date.now();

  // 等待 ffmpeg 生成 index.m3u8（新会话需等待转码启动，复用会话文件已存在）
  const m3u8Path = path.join(session.dir, 'index.m3u8');
  const waitStart = Date.now();
  const WAIT_TIMEOUT = 15000;

  function tryReadM3U8() {
    if (!transcodeSessions.has(sessionId)) {
      return sendJSON(res, 502, { error: '转码进程已退出，无法生成播放列表（请检查频道源是否可用）' });
    }
    fs.readFile(m3u8Path, 'utf-8', (err, data) => {
      if (err) {
        if (Date.now() - waitStart > WAIT_TIMEOUT) {
          cleanupTranscodeSession(sessionId);
          return sendJSON(res, 502, { error: '转码启动超时（15s 内未生成播放列表），请检查频道源是否可用' });
        }
        return setTimeout(tryReadM3U8, 200);
      }
      setCorsHeaders(res);
      res.setHeader('Content-Type', 'application/vnd.apple.mpegurl; charset=utf-8');
      res.setHeader('Cache-Control', 'no-cache');
      res.writeHead(200);
      res.end(rewriteTranscodeM3U8(data, sessionId));
    });
  }
  tryReadM3U8();
}

/**
 * /transcode-seg/<sessionId>/<filename> 端点处理：返回会话临时目录中的分片文件
 */
function handleTranscodeSeg(req, res, pathname) {
  const m = pathname.match(/^\/transcode-seg\/([0-9a-f]+)\/([A-Za-z0-9_.-]+)$/);
  if (!m) {
    return sendJSON(res, 400, { error: '无效的分片路径' });
  }
  const sessionId = m[1];
  const filename = m[2];
  const session = transcodeSessions.get(sessionId);
  if (!session) {
    return sendJSON(res, 404, { error: '转码会话不存在或已过期' });
  }
  session.lastAccess = Date.now();
  const filePath = path.join(session.dir, filename);
  fs.stat(filePath, (err, stats) => {
    if (err || !stats.isFile()) {
      return sendJSON(res, 404, { error: '分片不存在（可能已被清理）' });
    }
    setCorsHeaders(res);
    res.setHeader('Content-Type', 'video/mp2t');
    res.setHeader('Cache-Control', 'no-cache');
    res.writeHead(200);
    const rs = fs.createReadStream(filePath);
    rs.pipe(res);
    // 客户端断开时销毁文件读取流，避免向已关闭连接写入触发异常
    res.on('close', () => { rs.destroy(); });
  });
}

// 定时清理超时转码会话：每 60s 扫描，超过 5 分钟无访问则 kill 进程并删除目录
setInterval(() => {
  const now = Date.now();
  for (const [id, s] of transcodeSessions) {
    if (now - s.lastAccess > TRANSCODE_IDLE_MS) {
      cleanupTranscodeSession(id);
    }
  }
}, 60 * 1000);

// 进程退出时清理所有转码会话，避免残留 ffmpeg 孤儿进程
process.on('exit', () => {
  for (const id of Array.from(transcodeSessions.keys())) {
    cleanupTranscodeSession(id);
  }
});

/**
 * 静态文件服务
 */
function serveStatic(req, res, pathname) {
  if (pathname === '/') pathname = '/html/player.html';

  // 防止目录遍历
  const safePath = path.normalize(pathname).replace(/^(\.\.[\/\\])+/, '');
  const filePath = path.join(ROOT_DIR, safePath);

  if (filePath !== ROOT_DIR && !filePath.startsWith(ROOT_DIR + path.sep)) {
    res.writeHead(403);
    return res.end('Forbidden');
  }

  fs.stat(filePath, (err, stats) => {
    if (err || !stats.isFile()) {
      res.writeHead(404, { 'Content-Type': 'text/plain; charset=utf-8' });
      return res.end('404 Not Found');
    }
    const ext = path.extname(filePath).toLowerCase();
    const mime = MIME_TYPES[ext] || 'application/octet-stream';
    res.writeHead(200, { 'Content-Type': mime });
    fs.createReadStream(filePath).pipe(res);
  });
}

/**
 * 主请求路由
 */
const server = http.createServer((req, res) => {
  let parsedUrl;
  try {
    parsedUrl = new URL(req.url, 'http://localhost');
  } catch (e) {
    res.writeHead(400);
    return res.end('Bad Request');
  }
  const pathname = parsedUrl.pathname;

  // CORS预检
  if (req.method === 'OPTIONS') {
    setCorsHeaders(res);
    res.writeHead(204);
    return res.end();
  }

  if (pathname === '/proxy' || pathname === '/playlist') {
    return handleProxy(req, res);
  }

  if (pathname === '/config') {
    return sendJSON(res, 200, { defaultPlaylist: DEFAULT_PLAYLIST, playlists: BUILTIN_PLAYLISTS });
  }

  // HEVC/H.265 转码相关端点
  if (pathname === '/transcode') {
    return handleTranscode(req, res);
  }
  if (pathname.indexOf('/transcode-seg/') === 0) {
    return handleTranscodeSeg(req, res, pathname);
  }
  if (pathname === '/transcode-status') {
    return sendJSON(res, 200, { enabled: ENABLE_TRANSCODE, ffmpegAvailable: ffmpegAvailable });
  }

  // 静态文件
  return serveStatic(req, res, pathname);
});

server.listen(PORT, () => {
  console.log('IPTV Live 代理服务器已启动: http://localhost:' + PORT);
  console.log('默认播放列表: ' + DEFAULT_PLAYLIST);
  if (BUILTIN_PLAYLISTS.length) {
    console.log('内置播放列表: ' + BUILTIN_PLAYLISTS.length + ' 个');
  }
  console.log('转码功能: ' + (ENABLE_TRANSCODE ? '已启用' : '未启用') + (ENABLE_TRANSCODE ? (ffmpegAvailable ? '（ffmpeg 可用）' : '（警告: 未检测到 ffmpeg）') : ''));
});