package obs

import (
	"context"

	ekitlogx "git.example.com/enjoydream/ekit/observability/logx"
	"github.com/oekit/logx"
)

// ToEkitLogx 把 oekit logx.Logger 适配为 agentkit v0.14.x 仍依赖的
// ekit 单体 logx.Logger。两侧接口形状一致，唯 Level/Attr 分属两包为不同
// 命名类型（Level 枚举同为 iota Debug=0..Error=3，Attr 同为 Key/Value 两字段，
// 按 int/逐字段转换）。agentkit 发版迁 oekit 后本桥整体退役。
func ToEkitLogx(l logx.Logger) ekitlogx.Logger {
	if l == nil {
		return nil
	}
	return ekitBridge{Logger: l}
}

type ekitBridge struct {
	logx.Logger
}

func (b ekitBridge) WithCtx(ctx context.Context) ekitlogx.Logger {
	return ekitBridge{b.Logger.WithCtx(ctx)}
}

func (b ekitBridge) WithService(name string) ekitlogx.Logger {
	return ekitBridge{b.Logger.WithService(name)}
}

func (b ekitBridge) WithAttrs(attrs ...ekitlogx.Attr) ekitlogx.Logger {
	oattrs := make([]logx.Attr, len(attrs))
	for i, a := range attrs {
		oattrs[i] = logx.Attr{Key: a.Key, Value: a.Value}
	}
	return ekitBridge{b.Logger.WithAttrs(oattrs...)}
}

func (b ekitBridge) WithGroup(name string) ekitlogx.Logger {
	return ekitBridge{b.Logger.WithGroup(name)}
}

func (b ekitBridge) SetLevel(level ekitlogx.Level) {
	b.Logger.SetLevel(logx.Level(level))
}
