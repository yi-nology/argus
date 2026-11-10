// observe_bridge.go —— 观测函数桥（v3.173.0 引擎/契约拆分的接线件）：
// hooks.go/observe.go 观测面留驻 internal/plugin（契约消费方 workflow 与引擎
// 共享同一观测出口）；引擎经函数值别名零改动引用。签名即契约：改上游函数
// 签名时此处编译期暴露。

package dispatch

import "github.com/yi-nology/argus/internal/plugin"

var (
	// ReportBestOfN best-of-N 采样结果分布（full/partial）。
	ReportBestOfN = plugin.ReportBestOfN
	// ReportLayerExec 分层执行探针（层被真正走到时 +1）。
	ReportLayerExec = plugin.ReportLayerExec
	// ReportPluginFailure 插件失败计数。
	ReportPluginFailure = plugin.ReportPluginFailure
	// ReportPluginSkipped 插件跳过原因分布。
	ReportPluginSkipped = plugin.ReportPluginSkipped
)
