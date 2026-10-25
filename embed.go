package main

import "embed"

// 嵌入静态资源目录（编译进单一二进制，运行时无需外部文件）
// 注意：js/hls.min.js 是浏览器端 HLS 播放库，必须一并嵌入
//
//go:embed html js img
var staticFS embed.FS