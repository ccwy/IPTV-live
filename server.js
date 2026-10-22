#!/usr/bin/env node
/**
 * IPTV Live 代理服务器（零外部依赖）
 * 功能：CORS代理（含M3U8重写）、静态文件服务
 */

const http = require('http');
const https = require('https');
const fs = require('fs');
const path = require('path');

const PORT = process.env.PORT || 8080;
const DEFAULT_PLAYLIST = 'https://iptv-org.github.io/iptv/index.m3u';
const ROOT_DIR = __dirname;

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

  const lib = target.protocol === 'https:' ? https : http;

  // 构造转发请求头
  const forwardHeaders = {};
  const copyHeaders = ['range', 'user-agent', 'accept', 'accept-language'];
  for (const h of copyHeaders) {
    if (req.headers[h]) forwardHeaders[h] = req.headers[h];
  }
  if (!forwardHeaders['user-agent']) {
    forwardHeaders['user-agent'] = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36';
  }
  forwardHeaders['host'] = target.host;
  forwardHeaders['accept-encoding'] = 'identity'; // 避免目标返回压缩内容导致解码失败

  const options = {
    hostname: target.hostname,
    port: target.port || (target.protocol === 'https:' ? 443 : 80),
    path: target.pathname + target.search,
    method: 'GET', // 代理只做GET
    headers: forwardHeaders,
    timeout: 30000,
  };

  const proxyReq = lib.request(options, (proxyRes) => {
    const contentType = proxyRes.headers['content-type'] || '';
    const statusCode = proxyRes.statusCode || 502;

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
        let body = Buffer.concat(chunks).toString('utf-8');
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
    sendJSON(res, 504, { error: '连接目标服务器超时(30s): ' + targetUrl });
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
 * 静态文件服务
 */
function serveStatic(req, res, pathname) {
  if (pathname === '/') pathname = '/player.html';

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

  // 静态文件
  return serveStatic(req, res, pathname);
});

server.listen(PORT, () => {
  console.log('IPTV Live 代理服务器已启动: http://localhost:' + PORT);
  console.log('默认播放列表: ' + DEFAULT_PLAYLIST);
  if (BUILTIN_PLAYLISTS.length) {
    console.log('内置播放列表: ' + BUILTIN_PLAYLISTS.length + ' 个');
  }
});