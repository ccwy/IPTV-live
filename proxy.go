package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// 环境变量布尔值读取（解析失败返回默认值）
func envBool(key string, def bool) bool {
	if v, err := strconv.ParseBool(os.Getenv(key)); err == nil {
		return v
	}
	return def
}

// SSRF 防护开关：默认禁止代理访问内网/保留地址段；
// 需代理局域网直播源时，可设环境变量 ALLOW_PRIVATE_NETWORK=true 放开
var allowPrivateNetwork = envBool("ALLOW_PRIVATE_NETWORK", false)

// 上游 TLS 证书校验开关：默认跳过校验（部分小众直播源使用自签名证书）；
// 设环境变量 UPSTREAM_TLS_SKIP_VERIFY=false 可开启严格证书校验
var upstreamInsecureSkipVerify = envBool("UPSTREAM_TLS_SKIP_VERIFY", true)

// m3u8 响应体最大字节数（防止恶意上游返回超大内容导致内存耗尽）
const maxM3U8Bytes = 10 * 1024 * 1024

// SSRF 拦截错误（连接内网/保留地址时返回）
var errSSRFBlocked = errors.New("target address is in a blocked private/reserved network range")

// 需要从目标响应中移除的头（会干扰 CORS 或缓存）
var stripHeaders = map[string]bool{
	"access-control-allow-origin":         true,
	"access-control-allow-methods":        true,
	"access-control-allow-headers":        true,
	"access-control-allow-credentials":    true,
	"access-control-expose-headers":       true,
	"access-control-max-age":              true,
	"content-security-policy":             true,
	"content-security-policy-report-only": true,
	"x-frame-options":                     true,
	"strict-transport-security":           true,
	"content-encoding":                    true,
	"transfer-encoding":                   true,
	"connection":                          true,
	"keep-alive":                          true,
}

// 本机地址匹配（referer 改写判断用）
var (
	reLocal192 = regexp.MustCompile(`^192\.168\.`)
	reLocal10  = regexp.MustCompile(`^10\.`)
	reLocal172 = regexp.MustCompile(`^172\.(1[6-9]|2\d|3[01])\.`)
)

// 上游请求客户端：禁用自动重定向（需按目标重建头并限制 5 次，故手动跟随）
var upstreamClient = &http.Client{
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
	Transport: &http.Transport{
		// 连接与响应头等待超时 60s（慢速源容易超时）；流式 body 读取不受总时长限制
		DialContext: (&net.Dialer{
			Timeout:   60 * time.Second,
			KeepAlive: 30 * time.Second,
			Control:   dialControl, // SSRF 防护：连接前校验目标 IP
		}).DialContext,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: upstreamInsecureSkipVerify}, // 放宽自签名证书校验，部分小众源自签名
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		DisableCompression:    true, // 请求已显式 Accept-Encoding: identity，禁止自动解压干扰头透传
		// 连接池扩容：hls.js 并发拉取分片时复用空闲连接，避免频繁重建连接造成交付抖动
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 20,
		IdleConnTimeout:     90 * time.Second,
		// 启用 HTTP/2 多路复用：https 上游所有分片请求共享单连接，避免每分片新建 TCP/TLS 连接的冷启动延迟与并发抖动
		ForceAttemptHTTP2:   true,
	},
}

// 响应记录器：跟踪响应头是否已写出（sendJSON 防重复写，等价原版 headersSent 检查）
type responseRecorder struct {
	http.ResponseWriter
	wroteHeader bool
}

func (r *responseRecorder) WriteHeader(code int) {
	if r.wroteHeader {
		return
	}
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	return r.ResponseWriter.Write(b)
}

// Flush 转发至底层 writer（若支持），保证流式转发的实时性（直播无限流场景降低缓冲延迟）
func (r *responseRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// 代理请求处理
func handleProxy(w http.ResponseWriter, r *http.Request) {
	// 包装响应以跟踪写头状态
	rec := &responseRecorder{ResponseWriter: w}

	// 仅允许 GET/HEAD 方法（OPTIONS 已在 routeHandler 全局处理）
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		rec.Header().Set("Allow", "GET, HEAD")
		sendJSON(rec, http.StatusMethodNotAllowed, map[string]string{"error": "仅支持 GET/HEAD 方法"})
		return
	}

	targetUrlStr := r.URL.Query().Get("url")
	if targetUrlStr == "" {
		sendJSON(rec, http.StatusBadRequest, map[string]string{
			"error": "缺少 url 参数。用法: /proxy?url=<encodeURIComponent(目标URL)>",
		})
		return
	}

	target, err := url.Parse(targetUrlStr)
	if err != nil {
		sendJSON(rec, http.StatusBadRequest, map[string]string{"error": "无效的URL: " + targetUrlStr})
		return
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		sendJSON(rec, http.StatusBadRequest, map[string]string{"error": "仅支持 http/https 协议"})
		return
	}

	// 构造基础转发头（含 referer/cookie），具体请求头在 fetchUpstream 中按目标重建
	baseHeaders := buildForwardHeaders(r)
	fetchUpstream(rec, r, targetUrlStr, 0, baseHeaders)
}

// 构造转发请求头：复制必要头（含 referer/cookie），固定使用 APTV 播放器 UA
func buildForwardHeaders(r *http.Request) http.Header {
	forwardHeaders := http.Header{}
	for _, name := range []string{"Range", "Accept", "Accept-Language", "Referer", "Cookie"} {
		if v := r.Header.Get(name); v != "" {
			forwardHeaders.Set(name, v)
		}
	}
	// 固定 APTV（Apple TV 播放器）UA：部分直播源仅允许特定播放器 UA 访问
	forwardHeaders.Set("User-Agent", "APTV/1.0")
	return forwardHeaders
}

// 递归请求上游：跟随 301/302/303/307/308 重定向，最多 5 次
func fetchUpstream(rec *responseRecorder, r *http.Request, targetUrlStr string, depth int, baseHeaders http.Header) {
	if depth > 5 {
		sendJSON(rec, http.StatusBadGateway, map[string]string{
			"error": "重定向次数过多",
			"url":   targetUrlStr,
		})
		return
	}

	target, err := url.Parse(targetUrlStr)
	if err != nil {
		sendJSON(rec, http.StatusBadRequest, map[string]string{"error": "无效的重定向URL: " + targetUrlStr})
		return
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		sendJSON(rec, http.StatusBadRequest, map[string]string{"error": "重定向目标仅支持 http/https 协议"})
		return
	}

	// 每次请求基于当前目标重建头（host/referer 随重定向更新）
	forwardHeaders := baseHeaders.Clone()
	// referer 改写：本站地址（localhost/本机IP）改写为目标源同域 referer，避免源服务器校验失败返回 403
	if ref := forwardHeaders.Get("Referer"); ref != "" {
		if refURL, refErr := url.Parse(ref); refErr == nil {
			refHost := refURL.Hostname()
			if isLocalHost(refHost) {
				forwardHeaders.Set("Referer", target.Scheme+"://"+target.Host+"/")
			}
		} else {
			forwardHeaders.Del("Referer")
		}
	}
	forwardHeaders.Set("Accept-Encoding", "identity") // 避免目标返回压缩内容导致解码失败

	// 构造上游 GET 请求；context 绑定客户端连接，客户端断开时自动中止上游请求
	upReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, targetUrlStr, nil)
	if err != nil {
		sendJSON(rec, http.StatusBadRequest, map[string]string{"error": "无效的请求URL: " + targetUrlStr})
		return
	}
	upReq.Header = forwardHeaders
	upReq.Host = target.Host // Host 头设为目标 host

	resp, err := upstreamClient.Do(upReq)
	if err != nil {
		// 客户端已断开则不再写响应
		if r.Context().Err() != nil {
			return
		}
		// SSRF 拦截：目标地址属于内网/保留地址段
		if errors.Is(err, errSSRFBlocked) {
			sendJSON(rec, http.StatusForbidden, map[string]string{
				"error": "目标地址属于内网或保留地址段，禁止代理访问（如需代理局域网源，设置环境变量 ALLOW_PRIVATE_NETWORK=true）",
			})
			return
		}
		if isTimeoutErr(err) {
			sendJSON(rec, http.StatusGatewayTimeout, map[string]string{
				"error": "连接目标服务器超时(60s): " + targetUrlStr,
			})
		} else {
			sendJSON(rec, http.StatusBadGateway, map[string]string{
				"error": "无法连接目标服务器: " + err.Error(),
				"url":   targetUrlStr,
			})
		}
		return
	}

	// 跟随重定向：读取 Location 头拼接新地址后递归请求，最多 5 次（防死循环）
	if isRedirectStatus(resp.StatusCode) {
		location := resp.Header.Get("Location")
		_, _ = io.Copy(io.Discard, resp.Body) // 丢弃重定向响应体，不转发 body
		resp.Body.Close()
		if location == "" {
			sendJSON(rec, http.StatusBadGateway, map[string]string{
				"error": "重定向响应缺少 Location 头",
				"url":   targetUrlStr,
			})
			return
		}
		nextUrl, err := resolveLocation(location, targetUrlStr)
		if err != nil {
			sendJSON(rec, http.StatusBadGateway, map[string]string{"error": "无效的重定向地址: " + location})
			return
		}
		fetchUpstream(rec, r, nextUrl, depth+1, baseHeaders)
		return
	}

	// 判断是否需要重写 m3u8 内容（Content-Type 或 URL 后缀）
	contentType := resp.Header.Get("Content-Type")
	urlPath := strings.ToLower(target.Path)
	if isM3U8ContentType(contentType) || strings.HasSuffix(urlPath, ".m3u8") || strings.HasSuffix(urlPath, ".m3u") {
		handleM3U8Response(rec, resp, targetUrlStr, contentType) // 内部负责关闭 body
	} else {
		streamResponse(rec, resp) // 流式转发（视频段等大文件），内部负责关闭 body
	}
}

// 判断是否重定向状态码
func isRedirectStatus(code int) bool {
	return code == 301 || code == 302 || code == 303 || code == 307 || code == 308
}

// 解析 Location（可为相对路径），基于当前目标 URL 拼接为绝对地址
func resolveLocation(location, base string) (string, error) {
	locURL, err := url.Parse(location)
	if err != nil {
		return "", err
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	return baseURL.ResolveReference(locURL).String(), nil
}

// 判断错误是否为超时（连接超时/响应头等待超时）
func isTimeoutErr(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return false
}

// 判断 host 是否本机地址（localhost/回环/内网网段）
func isLocalHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "0.0.0.0" || host == "::1" ||
		reLocal192.MatchString(host) || reLocal10.MatchString(host) || reLocal172.MatchString(host)
}

// 判断 IP 是否属于禁止代理访问的内网/保留地址段
func isBlockedIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		// 0.0.0.0/8（本网络段）
		if ip4[0] == 0 {
			return true
		}
		// 100.64.0.0/10（CGNAT，云厂商元数据服务常用，如阿里云 100.100.100.200）
		if ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
			return true
		}
		// 192.0.0.0/24、192.0.2.0/24（TEST-NET）
		if ip4[0] == 192 && ip4[1] == 0 && (ip4[2] == 0 || ip4[2] == 2) {
			return true
		}
		// 192.88.99.0/24（6to4 中继任播，RFC 7526 已废弃）
		if ip4[0] == 192 && ip4[1] == 88 && ip4[2] == 99 {
			return true
		}
		// 198.18.0.0/15（基准测试）
		if ip4[0] == 198 && (ip4[1] == 18 || ip4[1] == 19) {
			return true
		}
		// 198.51.100.0/24、203.0.113.0/24（TEST-NET-2/3）
		if ip4[0] == 198 && ip4[1] == 51 && ip4[2] == 100 {
			return true
		}
		if ip4[0] == 203 && ip4[1] == 0 && ip4[2] == 113 {
			return true
		}
		// 240.0.0.0/4（保留）与 255.255.255.255（广播）
		if ip4[0] >= 240 {
			return true
		}
	} else {
		// 6to4 隧道 2002::/16：第 2-5 字节为嵌入的 IPv4 地址，递归校验防止内网 IPv4 借道绕过
		// （如 2002:0a00:1:: 嵌入 10.0.0.1）
		if ip[0] == 0x20 && ip[1] == 0x02 {
			return isBlockedIP(net.IP(ip[2:6]))
		}
		// Teredo 隧道 2001:0000::/32：可经中继访问内网，直接拦截
		if ip[0] == 0x20 && ip[1] == 0x01 && ip[2] == 0x00 && ip[3] == 0x00 {
			return true
		}
		// 2001:db8::/32（文档示例）、2001:2::/48（基准测试）、2001:10::/28（ORCHID）
		if ip[0] == 0x20 && ip[1] == 0x01 {
			if ip[2] == 0x0d && ip[3] == 0xb8 {
				return true
			}
			if ip[2] == 0x00 && ip[3] == 0x02 {
				return true
			}
			if ip[2] == 0x00 && ip[3] >= 0x10 && ip[3] <= 0x1f {
				return true
			}
		}
		// 白名单兜底：仅放行 2000::/3 全球单播段，其余全部拦截。
		// 覆盖：::/96（IPv4-compatible，可表示内网 IPv4 如 ::10.0.0.1）、
		// 100::/64（discard）、fec0::/10（站点本地，已废弃）、
		// 64:ff9b::/96（NAT64，可嵌入 IPv4）、0000::/8、4000::/3 等全部保留/未分配段
		if (ip[0] & 0xe0) != 0x20 {
			return true
		}
	}
	return false
}

// 连接建立前校验目标 IP（DNS 解析后、TCP 连接前调用，可防 DNS rebinding）
func dialControl(network, address string, c syscall.RawConn) error {
	if allowPrivateNetwork {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("invalid IP address: %s", host)
	}
	if isBlockedIP(ip) {
		return errSSRFBlocked
	}
	return nil
}

// 缓冲并重写 m3u8 响应
func handleM3U8Response(rec *responseRecorder, resp *http.Response, baseUrl, contentType string) {
	defer resp.Body.Close()

	rawBuf, err := io.ReadAll(io.LimitReader(resp.Body, maxM3U8Bytes+1))
	if err != nil {
		sendJSON(rec, http.StatusBadGateway, map[string]string{"error": "目标服务器响应错误: " + err.Error()})
		return
	}
	if len(rawBuf) > maxM3U8Bytes {
		sendJSON(rec, http.StatusBadGateway, map[string]string{"error": "目标 m3u8 响应体过大，已拒绝处理"})
		return
	}

	// 编码容错：UTF-8 无效说明源内容非 UTF-8，
	// 放弃重写直接透传原始字节（不重写比写坏好）
	if !utf8.Valid(rawBuf) {
		setCorsHeaders(rec)
		ct := contentType
		if ct == "" {
			ct = "application/vnd.apple.mpegurl"
		}
		rec.Header().Set("Content-Type", ct)
		rec.Header().Set("Content-Length", strconv.Itoa(len(rawBuf)))
		// 禁用浏览器缓存：直播播放列表必须每次拉取最新版本，旧播放列表会导致播放跳帧
		rec.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		rec.Header().Set("Pragma", "no-cache")
		rec.WriteHeader(status304to200(resp.StatusCode))
		_, _ = rec.Write(rawBuf)
		return
	}

	body := string(rawBuf)
	actuallyM3U8 := looksLikeM3U8(body)
	if actuallyM3U8 {
		body = rewriteM3U8(body, baseUrl)
	}
	setCorsHeaders(rec)
	if actuallyM3U8 {
		rec.Header().Set("Content-Type", "application/vnd.apple.mpegurl; charset=utf-8")
	} else {
		ct := contentType
		if ct == "" {
			ct = "text/plain; charset=utf-8"
		}
		rec.Header().Set("Content-Type", ct)
	}
	rec.Header().Set("Content-Length", strconv.Itoa(len(body)))
	// 禁用浏览器缓存：直播播放列表必须每次拉取最新版本，旧播放列表会导致播放跳帧
	rec.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	rec.Header().Set("Pragma", "no-cache")
	rec.WriteHeader(status304to200(resp.StatusCode))
	_, _ = rec.Write([]byte(body))
}

// 流式转发非 m3u8 响应（视频段等大文件，支持 Range/seek）
func streamResponse(rec *responseRecorder, resp *http.Response) {
	defer resp.Body.Close()

	// 过滤目标响应头，移除干扰 CORS 的头（保留 Content-Length、Content-Range、Accept-Ranges 等）
	filtered := filterHeaders(resp.Header)
	setCorsHeaders(rec)
	for k, vs := range filtered {
		for _, v := range vs {
			rec.Header().Add(k, v)
		}
	}
	rec.WriteHeader(status304to200(resp.StatusCode))
	// 流式拷贝响应体并累积刷新，降低直播流缓冲延迟；客户端断开或上游读取错误时静默结束
	buf := make([]byte, 256*1024)
	pending := 0
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := rec.Write(buf[:n]); werr != nil {
				return
			}
			pending += n
			// 累积足够数据后再刷新，减少小包与 syscall 开销，同时保持流式实时性
			if pending >= 128*1024 {
				rec.Flush()
				pending = 0
			}
		}
		if readErr != nil {
			if pending > 0 {
				rec.Flush()
			}
			return
		}
	}
}

// 过滤目标响应头，移除干扰 CORS 的头
func filterHeaders(headers http.Header) http.Header {
	result := http.Header{}
	for k, vs := range headers {
		if stripHeaders[strings.ToLower(k)] {
			continue
		}
		result[k] = vs
	}
	return result
}

// 状态码 304 转为 200（m3u8 缓冲路径：304 无 body，统一按 200 返回）
func status304to200(code int) int {
	if code == http.StatusNotModified {
		return http.StatusOK
	}
	return code
}