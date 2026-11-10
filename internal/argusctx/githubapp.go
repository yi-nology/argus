// GitHub App installation token support。
// 用 App ID + Private Key 签发 JWT（RS256，纯标准库），再调 GitHub API 换取 installation access token（1h 有效期）。
// token 供给走 go-git-platform v0.100+ 的 provider.TokenSource：transport 每请求取值，
// 临期懒续期，不再需要后台 ticker + provider 重建（旧方案的过期 token 在飞窗口归零）。
package argusctx

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/config"
)

// githubAppHTTPClient GitHub API 调用共享客户端（包级复用连接池——每次换 token
// 新建 Client 会丢弃 TLS 连接池，installation token 每小时刷新一次即可复用）。
var githubAppHTTPClient = &http.Client{Timeout: 15 * time.Second}

// generateInstallationToken 用 GitHub App 凭证换取 installation access token。
// https://docs.github.com/en/rest/apps/apps?apiVersion=2022-11-28#create-an-installation-access-token-for-an-app
func generateInstallationToken(appID int64, privateKeyPEM string, installationID int64) (string, time.Time, error) {
	// 解析私钥（支持 raw PEM 和 base64 编码两种输入）
	keyData := []byte(privateKeyPEM)
	if !strings.Contains(privateKeyPEM, "BEGIN") {
		decoded, err := base64.StdEncoding.DecodeString(privateKeyPEM)
		if err != nil {
			return "", time.Time{}, fmt.Errorf("base64 解码私钥失败: %w", err)
		}
		keyData = decoded
	}
	block, _ := pem.Decode(keyData)
	if block == nil {
		return "", time.Time{}, fmt.Errorf("PEM 解析失败：无有效块")
	}
	parsedKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		// 尝试 PKCS8 格式
		key, err2 := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err2 != nil {
			return "", time.Time{}, fmt.Errorf("解析私钥失败 (PKCS1: %v, PKCS8: %v)", err, err2)
		}
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return "", time.Time{}, fmt.Errorf("非 RSA 私钥: %T", key)
		}
		parsedKey = rsaKey
	}

	// 签发 JWT（RS256，标准库实现，无外部依赖）
	now := time.Now()
	jwtString, err := signJWT(appID, now, parsedKey)
	if err != nil {
		return "", time.Time{}, err
	}

	// 调 GitHub API 换取 installation token
	url := fmt.Sprintf("https://api.github.com/app/installations/%d/access_tokens", installationID)
	req, err := http.NewRequest("POST", url, nil)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwtString)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := githubAppHTTPClient.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("请求 GitHub API 失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return "", time.Time{}, fmt.Errorf("GitHub API 返回 %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", time.Time{}, fmt.Errorf("解析响应失败: %w", err)
	}
	if result.Token == "" {
		return "", time.Time{}, fmt.Errorf("GitHub 返回空 token")
	}
	return result.Token, result.ExpiresAt, nil
}

// signJWT 用 RSA-SHA256 签发 GitHub App JWT（纯标准库，无外部依赖）。
// header: {"alg":"RS256","typ":"JWT"}；claims: {"iat":..,"exp":..,"iss":..}。
func signJWT(appID int64, now time.Time, key *rsa.PrivateKey) (string, error) {
	// header
	headerJSON, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	// claims：iat 回拨 10s 容差，exp = iat + 10min（GitHub 允许最长 10 分钟）
	iat := now.Add(-10 * time.Second).Unix()
	exp := now.Add(9 * time.Minute).Unix() // 留 1min 余量
	claimsJSON, _ := json.Marshal(map[string]int64{"iat": iat, "exp": exp, "iss": appID})

	enc := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	payload := enc(headerJSON) + "." + enc(claimsJSON)

	// RS256 签名
	hash := sha256.Sum256([]byte(payload))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
	if err != nil {
		return "", fmt.Errorf("RSA 签名失败: %w", err)
	}
	return payload + "." + enc(sig), nil
}

// tokenRefreshAhead installation token 剩余寿命低于该阈值时在取值路径上同步续期
// （token 有效期 1h，即每小时至多一次取值路径上的额外 API 往返）。
const tokenRefreshAhead = 5 * time.Minute

// installationTokenSource provider.TokenSource 实现（go-git-platform v0.100+）：
// transport 每请求经此取 token，临期懒续期 ⇒ 轮换即刻生效，无需重建 provider。
// 并发取值合并到同一把锁（每小时至多一次 ≤15s 的同步刷新，其余请求排队等新值）；
// 续期失败且旧 token 仍在寿命内时退回旧值（可用性优先，下次请求再试）。
type installationTokenSource struct {
	appID          int64
	privateKeyPEM  string
	installationID int64
	log            logx.Logger

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

// newInstallationTokenSource 以启动期已获取的 token 初始化（启动失败即该平台判死，
// fail-fast 语义与旧方案一致；此后的续期全部走 Token() 懒路径）。
func newInstallationTokenSource(pc config.PlatformConfig, token string, expires time.Time,
	log logx.Logger) *installationTokenSource {
	return &installationTokenSource{
		appID:          pc.GitHubAppID,
		privateKeyPEM:  pc.GitHubAppPrivateKey,
		installationID: pc.GitHubAppInstallationID,
		log:            log,
		token:          token,
		expiresAt:      expires,
	}
}

// Token 每请求取值（provider.TokenSource 契约，并发安全）。
func (ts *installationTokenSource) Token(_ context.Context) (string, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.token != "" && time.Until(ts.expiresAt) > tokenRefreshAhead {
		return ts.token, nil
	}
	token, expires, err := generateInstallationToken(
		ts.appID, ts.privateKeyPEM, ts.installationID)
	if err != nil {
		if ts.token != "" && time.Until(ts.expiresAt) > 0 {
			ts.log.Error("argus.githubapp.token_refresh_failed", "error", err.Error(),
				"hint", "旧 token 仍在寿命内，本次请求沿用旧值，下次请求再试")
			return ts.token, nil
		}
		return "", fmt.Errorf("github app: installation token 获取失败: %w", err)
	}
	ts.token, ts.expiresAt = token, expires
	ts.log.Info("argus.githubapp.token_refreshed", "expires_at", expires.Format(time.RFC3339))
	return ts.token, nil
}
