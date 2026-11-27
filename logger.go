package main

import (
	"log"
	"os"
	"strings"
)

// 日志级别：由环境变量 LOG_LEVEL 控制后端日志输出量，避免长时间播放时容器日志无限累积。
// 取值（不区分大小写）：debug < info < warn < error < off。默认 info。
//   - debug：输出每个请求/切片的调试日志（排查问题用，最啰嗦）
//   - info ：输出启动信息 + 告警/错误（推荐日常使用，不打印逐请求日志）
//   - warn ：仅输出告警与错误
//   - error：仅输出错误
//   - off  ：关闭常规日志（Fatal 除外）
type logLevel int

const (
	levelDebug logLevel = iota
	levelInfo
	levelWarn
	levelError
	levelOff
)

func parseLogLevel(s string) logLevel {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return levelDebug
	case "info", "":
		return levelInfo
	case "warn", "warning":
		return levelWarn
	case "error":
		return levelError
	case "off", "none", "silent", "disable", "disabled":
		return levelOff
	default:
		return levelInfo
	}
}

var currentLogLevel = parseLogLevel(os.Getenv("LOG_LEVEL"))

func logEnabled(l logLevel) bool { return l >= currentLogLevel }

func logDebugf(format string, args ...any) {
	if logEnabled(levelDebug) {
		log.Printf("[DEBUG] "+format, args...)
	}
}

func logInfof(format string, args ...any) {
	if logEnabled(levelInfo) {
		log.Printf("[INFO] "+format, args...)
	}
}

func logWarnf(format string, args ...any) {
	if logEnabled(levelWarn) {
		log.Printf("[WARN] "+format, args...)
	}
}

func logErrorf(format string, args ...any) {
	if logEnabled(levelError) {
		log.Printf("[ERROR] "+format, args...)
	}
}
