package argusctx

import (
	"net/http"

	"github.com/yi-nology/go-git-platform/provider"
)

// ---------- webhook ----------

// DeliveryHeaders 各平台 delivery ID 请求头（§5.2：幂等键 = 平台 delivery ID，
// 在 ParseWebhookEvent 之前读取；SDK 的 NormalizedEvent.ID 是接收时刻 UnixNano，不可用）。
var DeliveryHeaders = []string{
	"X-Gitea-Delivery",
	"X-GitHub-Delivery",
	"X-Gitlab-Event-UUID",
}

// ExtractDeliveryID 从请求头提取平台 delivery ID；缺失返回空串（调用方 event_id 置 NULL + 告警）。
func (s *SDKCtx) ExtractDeliveryID(headerGetter func(key string) string) string {
	for _, h := range DeliveryHeaders {
		if v := headerGetter(h); v != "" {
			return v
		}
	}
	return ""
}

// ParseWebhook 解析平台原生 webhook（内部先做签名校验；secret 为空时 SDK 会跳过校验，
// 由调用方在配置侧保证 secret 语义）。r 可为从 hertz 原始字节重建的 *http.Request。
func (s *SDKCtx) ParseWebhook(platform string, r *http.Request, secret string) (*provider.NormalizedEvent, error) {
	p, err := s.Provider(platform)
	if err != nil {
		return nil, err
	}
	return p.ParseWebhookEvent(r, secret)
}
