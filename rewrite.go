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

// 判断内容是否 m3u8（前 512 字符 trim 后以 m3u8 标签开头）
func looksLikeM3U8(content string) bool {
	if content == "" {
		return false
	}
	runes := []rune(content)
	if len(runes) > 512 {
		runes = runes[:512]
	}
	head := strings.TrimSpace(string(runes))
	return strings.HasPrefix(head, "#EXTM3U") || strings.HasPrefix(head, "#EXTINF") || strings.HasPrefix(head, "#EXT-X")
}

// 将 m3u8/m3u 内容中的 URL 重写为代理 URL
func rewriteM3U8(content, baseUrl string) string {
	if content == "" {
		return content
	}
	lines := lineSplitRe.Split(content, -1)

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
			rewritten[i] = "/proxy?url=" + encodeURIComponent(absolute)
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
			return `URI="/proxy?url=` + encodeURIComponent(absolute) + `"`
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