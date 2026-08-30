package plugin

import (
	"sync"
	"time"
)

// 熔断参数（design §10.3：连续 3 次失败 → 熔断 5 分钟 → 半开探测 1 次成功即恢复）。
const (
	BreakerTripThreshold = 3
	BreakerCooldown      = 5 * time.Minute
)

// breaker 单插件熔断器：closed → open(冷却) → half-open(1 次探测) → closed/reopen。
type breaker struct {
	mu          sync.Mutex
	consecutive int
	openUntil   time.Time
	probing     bool
}

// Allow 返回是否放行；半开时仅放行一个探测请求。
func (b *breaker) Allow(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.openUntil.IsZero() {
		return true // closed
	}
	if now.Before(b.openUntil) {
		return false // 熔断中
	}
	if b.probing {
		return false // 半开探测已在途，不放行第二个
	}
	b.probing = true // 冷却期已过 → 半开，放行一个探测
	return true
}

// OnSuccess 关闭熔断。
func (b *breaker) OnSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.consecutive = 0
	b.openUntil = time.Time{}
	b.probing = false
}

// OnFailure 记录失败；达阈值开熔断，半开探测失败重新熔断。
func (b *breaker) OnFailure(now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.consecutive++
	if b.probing {
		// 探测失败：重新熔断
		b.openUntil = now.Add(BreakerCooldown)
		b.probing = false
		b.consecutive = BreakerTripThreshold
		return
	}
	if b.consecutive >= BreakerTripThreshold {
		b.openUntil = now.Add(BreakerCooldown)
	}
}

// Opened 是否处于熔断中。
func (b *breaker) Opened(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.openUntil.IsZero() && now.Before(b.openUntil)
}

// Breakers 全部插件的熔断板。
type Breakers struct {
	mu   sync.Mutex
	byNP map[string]*breaker
	Now  func() time.Time
}

func NewBreakers() *Breakers {
	return &Breakers{byNP: map[string]*breaker{}, Now: time.Now}
}

func (bs *Breakers) get(name string) *breaker {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	b, ok := bs.byNP[name]
	if !ok {
		b = &breaker{}
		bs.byNP[name] = b
	}
	return b
}

func (bs *Breakers) Allow(name string) bool  { return bs.get(name).Allow(bs.Now()) }
func (bs *Breakers) Success(name string)     { bs.get(name).OnSuccess() }
func (bs *Breakers) Failure(name string)     { bs.get(name).OnFailure(bs.Now()) }
func (bs *Breakers) Opened(name string) bool { return bs.get(name).Opened(bs.Now()) }
