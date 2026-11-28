package main

import (
	"bytes"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// M3U8 相关 Content-Type
var m3u8ContentTypes = []string{
	"application/vnd.apple.mpegurl",
	"application/x-mpegurl",
	"application/mpegurl",
	"text/plain", // 有些服务器用 text/plain 返回 m3u8，需结合内容判断
}

// 行分隔（兼容 \r\n 与 \n）
var lineSplitRe = regexp.MustCompile(`\r?\n`)

// URI="..." 属性匹配（如 EXT-X-KEY、EXT-X-MAP、EXT-X-MEDIA、EXT-X-STREAM-INF）
var uriAttrRe = regexp.MustCompile(`URI="([^"]+)"`)

// 判断 Content-Type 是否 m3u8 相关
func isM3U8ContentType(contentType string) bool {
	if contentType == "" {
		return false
	}
	ct := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	for _, t := range m3u8ContentTypes {
		if ct == t {
			return true
		}
	}
	return strings.Contains(ct, "mpegurl")
}

// 视频分片和密钥的 URL 后缀白名单（Path 不含 query，后缀匹配可靠）
var streamSegmentExts = []string{".ts", ".m4s", ".cmfv", ".mp4", ".key", ".aac"}

// isMediaContentType 媒体内容类型白名单：只有这些类型才会被原样透传给浏览器，其余一律降级为
// application/octet-stream。采用白名单（而非黑名单）——不在名单内的一律视为不安全，从根本上
// 杜绝未知/变体类型（text/html、application/x-javascript、image/svg+xml……）造成的反射 XSS。
func isMediaContentType(ct string) bool {
	if ct == "" {
		return false
	}
	base := strings.ToLower(strings.TrimSpace(strings.Split(ct, ";")[0]))
	if strings.HasPrefix(base, "video/") || strings.HasPrefix(base, "audio/") {
		return true
	}
	switch base {
	case "application/octet-stream", "binary/octet-stream",
		"application/vnd.apple.mpegurl", "application/x-mpegurl", "application/mpegurl":
		return true
	}
	return false
}

// isAllowedStreamContent 判断非 m3u8 响应是否为合法的直播流分片或加密密钥。
// 白名单策略：仅当上游 Content-Type 命中媒体白名单时放行；Content-Type 缺失时按分片后缀兜底放行；
// 其余（任何非媒体类型，无论 URL 后缀）一律拒绝。对外 Content-Type 还会再经 safeStreamContentType
// 白名单收敛，构成双层白名单防护。
func isAllowedStreamContent(contentType, urlPath string) bool {
	ct := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	// 1. Content-Type 命中媒体白名单 → 放行
	if isMediaContentType(ct) {
		return true
	}
	// 2. Content-Type 缺失（部分源不返回）→ 按分片后缀兜底放行
	if ct == "" {
		for _, ext := range streamSegmentExts {
			if strings.HasSuffix(urlPath, ext) {
				return true
			}
		}
	}
	// 3. 其余（非媒体类型 / 非分片后缀）一律拒绝
	return false
}

// 判断内容是否 m3u8（前 512 字符 trim 后以 m3u8 标签开头）
func looksLikeM3U8(content string) bool {
	if content == "" {
		return false
	}
	runes := []rune(content)
	if len(runes) > 512 {
		runes = runes[:512]
	}
	head := strings.TrimSpace(strings.TrimPrefix(string(runes), "\ufeff"))
	return strings.HasPrefix(head, "#EXTM3U") || strings.HasPrefix(head, "#EXTINF") || strings.HasPrefix(head, "#EXT-X")
}

// 将 m3u8/m3u 内容中的 URL 重写为代理 URL
// 单次 m3u8 重写的最大行数：超过则放弃逐行重写（避免逐行 AES 加密导致 CPU 放大攻击）
const maxRewriteLines = 50000

func rewriteM3U8(content, baseUrl string) string {
	if content == "" {
		return content
	}
	// 去除 UTF-8 BOM，避免首行标签被误判为 URL
	content = strings.TrimPrefix(content, "\ufeff")
	lines := lineSplitRe.Split(content, -1)
	if len(lines) > maxRewriteLines {
		// 行数超限，判定为异常/滥用列表，原样返回不再逐行加密，防止 CPU 资源耗尽
		logWarnf("[M3U8] 列表行数超限(%d > %d)，跳过重写以防 CPU 放大 url=%s", len(lines), maxRewriteLines, baseUrl)
		return content
	}

	rewritten := make([]string, len(lines))
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			rewritten[i] = line
			continue
		}

		// 注释行：处理其中的 URI="..." 属性
		if strings.HasPrefix(trimmed, "#") {
			rewritten[i] = rewriteAttributes(trimmed, baseUrl)
			continue
		}

		// 非注释非空行 = URL 行（媒体段、子播放列表、或 m3u 频道流 URL）
		absolute := resolveUrl(trimmed, baseUrl)
		if absolute != "" {
			rewritten[i] = "/stream?s=" + encryptURL(absolute)
		} else {
			rewritten[i] = line
		}
	}

	return strings.Join(rewritten, "\n")
}

// 重写标签属性中的 URI="..."（如 EXT-X-KEY、EXT-X-MAP、EXT-X-MEDIA、EXT-X-STREAM-INF）
func rewriteAttributes(line, baseUrl string) string {
	return uriAttrRe.ReplaceAllStringFunc(line, func(match string) string {
		// 正则保证格式为 URI="..."，直接截取内部 URI
		uri := match[len(`URI="`) : len(match)-len(`"`)]
		absolute := resolveUrl(uri, baseUrl)
		if absolute != "" {
			return `URI="/stream?s=` + encryptURL(absolute) + `"`
		}
		return match
	})
}

// 基于 baseUrl 解析相对 URL 为绝对 URL；返回空串表示不处理（已是代理 URL 或解析失败）
func resolveUrl(relative, baseUrl string) string {
	if relative == "" {
		return ""
	}
	// 已经是代理 URL 就不重复处理
	if strings.HasPrefix(relative, "/proxy?url=") {
		return ""
	}
	if strings.HasPrefix(relative, "/stream?s=") {
		return ""
	}
	relURL, err := url.Parse(relative)
	if err != nil {
		return ""
	}
	baseURL, err := url.Parse(baseUrl)
	if err != nil {
		return ""
	}
	return baseURL.ResolveReference(relURL).String()
}

// encodeURIComponent：与 JavaScript 同名函数行为一致
// 不编码字符：A-Z a-z 0-9 - _ . ! ~ * ' ( )，其余按 UTF-8 字节 %XX 编码
func encodeURIComponent(s string) string {
	var buf bytes.Buffer
	for _, b := range []byte(s) {
		if (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') ||
			b == '-' || b == '_' || b == '.' || b == '!' || b == '~' || b == '*' || b == '\'' || b == '(' || b == ')' {
			buf.WriteByte(b)
		} else {
			fmt.Fprintf(&buf, "%%%02X", b)
		}
	}
	return buf.String()
}
