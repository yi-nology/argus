// GitHub App installation token support。
// 用 App ID + Private Key 签发 JWT（RS256，纯标准库），再调 GitHub API 换取 installation access token（1h 有效期）。
// SDK provider 不支持动态 token，故 token 过期时重建 provider 替换到 map。
package context

import (
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

	"argus/internal/config"
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

// githubAppRefresher 管理 GitHub App installation token 的周期刷新。
// token 有效期 1h，每 50 分钟刷新一次；刷新失败时保留旧 token（下次重试）。
type githubAppRefresher struct {
	mu          sync.Mutex
	cfg         config.PlatformConfig
	log         logx.Logger
	refreshFunc func(newToken string) // 刷新成功后回调（替换 provider）
	current     string
	expiresAt   time.Time
	stopCh      chan struct{}
}

func newGitHubAppRefresher(pc config.PlatformConfig, log logx.Logger, refreshFunc func(string)) *githubAppRefresher {
	return &githubAppRefresher{
		cfg:         pc,
		log:         log,
		refreshFunc: refreshFunc,
		stopCh:      make(chan struct{}),
	}
}

// initWithToken 使用已获取的 token 初始化（避免重复 API 调用）。
func (r *githubAppRefresher) initWithToken(token string, expires time.Time) {
	r.mu.Lock()
	r.current = token
	r.expiresAt = expires
	r.mu.Unlock()
}

// Start 启动后台刷新循环。如果 initWithToken 已设置 token 则跳过首次获取。
func (r *githubAppRefresher) Start() error {
	r.mu.Lock()
	hasToken := r.current != ""
	r.mu.Unlock()

	if !hasToken {
		token, expires, err := generateInstallationToken(
			r.cfg.GitHubAppID, r.cfg.GitHubAppPrivateKey, r.cfg.GitHubAppInstallationID)
		if err != nil {
			return fmt.Errorf("github app: 初始 token 获取失败: %w", err)
		}
		r.mu.Lock()
		r.current = token
		r.expiresAt = expires
		r.mu.Unlock()
		r.refreshFunc(token)
	}

	r.log.Info("argus.githubapp.token_ready",
		"app_id", r.cfg.GitHubAppID,
		"installation_id", r.cfg.GitHubAppInstallationID,
		"expires_at", r.expiresAt.Format(time.RFC3339))

	go r.loop()
	return nil
}

func (r *githubAppRefresher) loop() {
	ticker := time.NewTicker(50 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			r.refresh()
		case <-r.stopCh:
			return
		}
	}
}

func (r *githubAppRefresher) refresh() {
	token, expires, err := generateInstallationToken(
		r.cfg.GitHubAppID, r.cfg.GitHubAppPrivateKey, r.cfg.GitHubAppInstallationID)
	if err != nil {
		r.log.Error("argus.githubapp.token_refresh_failed", "error", err.Error(),
			"hint", "将在下次 tick 重试，旧 token 仍有效")
		return
	}
	r.mu.Lock()
	r.current = token
	r.expiresAt = expires
	r.mu.Unlock()
	r.refreshFunc(token)
	r.log.Info("argus.githubapp.token_refreshed", "expires_at", expires.Format(time.RFC3339))
}

func (r *githubAppRefresher) Stop() {
	close(r.stopCh)
}
