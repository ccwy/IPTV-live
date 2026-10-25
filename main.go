package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
)

// 配置常量
const (
	defaultPort = "8080"
)

// 播放列表项（/config 接口返回结构）
type playlistItem struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// MIME 类型表（静态文件服务）
var mimeTypes = map[string]string{
	".html": "text/html; charset=utf-8",
	".js":   "application/javascript; charset=utf-8",
	".css":  "text/css; charset=utf-8",
	".json": "application/json; charset=utf-8",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".svg":  "image/svg+xml",
	".ico":  "image/x-icon",
	".m3u":  "application/vnd.apple.mpegurl",
	".m3u8": "application/vnd.apple.mpegurl",
	".ts":   "video/mp2t",
	".mp4":  "video/mp4",
	".txt":  "text/plain; charset=utf-8",
}

// 内置播放列表（环境变量 PLAYLISTS 解析结果，空数组保证 JSON 输出 []）
var builtinPlaylists = []playlistItem{}

// 加载内置播放列表：环境变量 PLAYLISTS，JSON 数组格式 [{"name":"名称","url":"M3U地址"}]
// 解析失败时仅输出警告并忽略，不影响服务启动；只保留 name/url 均为字符串的项
func loadPlaylists() {
	env := os.Getenv("PLAYLISTS")
	if env == "" {
		return
	}
	var raw any
	if err := json.Unmarshal([]byte(env), &raw); err != nil {
		log.Println("PLAYLISTS 环境变量解析失败，已忽略: " + err.Error())
		return
	}
	arr, ok := raw.([]any)
	if !ok {
		log.Println("PLAYLISTS 环境变量不是 JSON 数组，已忽略")
		return
	}
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, nameOk := m["name"].(string)
		url, urlOk := m["url"].(string)
		if nameOk && urlOk {
			builtinPlaylists = append(builtinPlaylists, playlistItem{Name: name, URL: url})
		}
	}
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = defaultPort
	}
	loadPlaylists()

	http.HandleFunc("/", routeHandler)

	log.Printf("IPTV Live 代理服务器已启动: http://localhost:%s", port)
	if len(builtinPlaylists) > 0 {
		log.Printf("内置播放列表(PLAYLISTS): %d 个", len(builtinPlaylists))
	} else {
		log.Println("内置播放列表(PLAYLISTS): 未设置")
	}
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatal(err)
	}
}

// 主请求路由
func routeHandler(w http.ResponseWriter, r *http.Request) {
	pathname := r.URL.Path

	// CORS 预检：任意路径返回 204 + CORS 头
	if r.Method == http.MethodOptions {
		setCorsHeaders(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	switch pathname {
	case "/proxy", "/playlist":
		// 代理处理（两者行为相同）
		handleProxy(w, r)
	case "/config":
		sendJSON(w, http.StatusOK, map[string]any{
			"playlists": builtinPlaylists,
		})
	case "/health":
		// 健康检查端点（供外部探测，如编排平台或 docker healthcheck）
		setCorsHeaders(w)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	default:
		// 静态文件服务
		serveStatic(w, r, pathname)
	}
}

// 静态文件服务（资源已通过 go:embed 嵌入二进制）
func serveStatic(w http.ResponseWriter, r *http.Request, pathname string) {
	if pathname == "/" {
		pathname = "/html/player.html"
	}

	// 防止目录遍历：规范化路径，拒绝越界（embed.FS 本身亦无法越界，此处保持与原版一致返回 403）
	clean := path.Clean(pathname)
	if strings.HasPrefix(clean, "../") || clean == ".." {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("Forbidden"))
		return
	}
	clean = strings.TrimPrefix(clean, "/")

	data, err := staticFS.ReadFile(clean)
	if err != nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("404 Not Found"))
		return
	}

	ext := strings.ToLower(path.Ext(clean))
	mime := mimeTypes[ext]
	if mime == "" {
		mime = "application/octet-stream"
	}
	w.Header().Set("Content-Type", mime)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// 发送 JSON 响应（统一错误格式 {"error":"..."}，带 CORS 头）
func sendJSON(w http.ResponseWriter, status int, obj any) {
	// 响应头已写出则不再尝试写 JSON（与原版 headersSent 检查一致）
	if rec, ok := w.(*responseRecorder); ok && rec.wroteHeader {
		return
	}
	body, err := json.Marshal(obj)
	if err != nil {
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// 设置 CORS 响应头
func setCorsHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "*")
}