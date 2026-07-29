// Package auth 提供基于 HMAC-SHA256 的简单 JWT 认证和 License Key 校验功能。
// 用于 API 登录鉴权和软件授权验证，不依赖第三方认证库。
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// TokenExpiry JWT Token 的默认过期时间：14天。
const (
	TokenExpiry = 14 * 24 * time.Hour
)

// Claims JWT Token 的自定义载荷，包含用户名、创建时间和过期时间。
type Claims struct {
	Username  string `json:"u"` // 用户名
	CreatedAt int64  `json:"c"` // 创建时间（Unix 时间戳）
	ExpiresAt int64  `json:"e"` // 过期时间（Unix 时间戳）
}

// Authenticator JWT 认证器，持有 HMAC 密钥用于签名和验签。
type Authenticator struct {
	secret []byte // HMAC-SHA256 签名密钥
}

// New 创建认证器实例。
// 参数 secret: HMAC 签名密钥；若为空则使用默认开发密钥。
func New(secret string) *Authenticator {
	if secret == "" {
		secret = "liangzai-default-secret-change-in-prod"
	}
	return &Authenticator{secret: []byte(secret)}
}

// GenerateToken 为指定用户名生成 JWT Token。
// 格式：Base64(header).Base64(payload).Base64(signature)。
// 参数 username: 用户名。
// 返回 token 字符串和可能的编码错误。
func (a *Authenticator) GenerateToken(username string) (string, error) {
	now := time.Now()
	claims := Claims{
		Username:  username,
		CreatedAt: now.Unix(),
		ExpiresAt: now.Add(TokenExpiry).Unix(),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}

	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	bPayload := base64.RawURLEncoding.EncodeToString(payload)
	sig := a.sign(header + "." + bPayload)

	return header + "." + bPayload + "." + sig, nil
}

// VerifyToken 验证 JWT Token 的签名和有效期。
// 步骤：校验三段式格式→校验 HMAC 签名→Base64 解码 Payload→JSON 解析→检查过期。
// 参数 token: 待验证的 JWT 字符串。
// 返回解析后的 Claims 指针和可能的错误（格式错误/签名不匹配/已过期）。
func (a *Authenticator) VerifyToken(token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("invalid token format")
	}

	expectedSig := a.sign(parts[0] + "." + parts[1])
	if !hmac.Equal([]byte(parts[2]), []byte(expectedSig)) {
		return nil, errors.New("invalid signature")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode payload: %w", err)
	}

	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("unmarshal claims: %w", err)
	}

	if time.Now().Unix() > claims.ExpiresAt {
		return nil, errors.New("token expired")
	}

	return &claims, nil
}

// sign 使用 HMAC-SHA256 对数据进行签名，返回 Base64 编码的签名字符串。
func (a *Authenticator) sign(data string) string {
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(data))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// ValidateLicenseKey checks the license key HMAC signature
// License key format: "username:expireUnix:username:hmac_of_first_two"
func ValidateLicenseKey(key, secret string) bool {
	if secret == "" {
		secret = "lz-2026-license-secret"
	}
	parts := strings.Split(key, ":")
	if len(parts) != 3 {
		return false
	}
	username := parts[0]
	expireStr := parts[1]
	sig := parts[2]
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(username + ":" + expireStr))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if sig != expected {
		return false
	}
	expUnix, err := strconv.ParseInt(expireStr, 10, 64)
	if err != nil {
		return false
	}
	return time.Now().Unix() < expUnix
}
