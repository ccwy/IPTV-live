package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log"
)

// AES-256-GCM 密钥（32 字节）
var aesKey []byte

// 初始化 AES 密钥：每次启动随机生成 32 字节
// 重启后旧密文自动失效，用户需重新加载播放列表（刷新页面即可）
func initAesKey() {
	aesKey = make([]byte, 32)
	if _, err := rand.Read(aesKey); err != nil {
		log.Fatal("生成 AES 密钥失败: " + err.Error())
	}
	log.Println("流地址加密密钥: 随机生成（重启后旧 /stream 链接自动失效）")
}

// encryptURL 用 AES-256-GCM 加密 URL，返回 URL-safe base64 编码的密文
// 格式: base64(nonce + ciphertext)，nonce 长度 12 字节（GCM 标准）
func encryptURL(url string) string {
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return ""
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return ""
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return ""
	}
	// Seal 返回 nonce + ciphertext（append 到 nonce 后面）
	sealed := gcm.Seal(nonce, nonce, []byte(url), nil)
	return base64.URLEncoding.EncodeToString(sealed)
}

// decryptURL 解密 encryptURL 的输出，返回原始 URL
func decryptURL(encrypted string) (string, error) {
	data, err := base64.URLEncoding.DecodeString(encrypted)
	if err != nil {
		return "", errors.New("base64 解码失败: " + err.Error())
	}
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("密文过短")
	}
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", errors.New("AES-GCM 解密失败: " + err.Error())
	}
	return string(plaintext), nil
}
