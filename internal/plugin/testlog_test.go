package plugin

import (
	"context"

	"github.com/oekit/logx"
)

// testNopLogger logx.Logger 全接口空实现（单测用）。
type testNopLogger struct{}

func (testNopLogger) Debug(string, ...any)  {}
func (testNopLogger) Info(string, ...any)   {}
func (testNopLogger) Warn(string, ...any)   {}
func (testNopLogger) Error(string, ...any)  {}
func (testNopLogger) Debugf(string, ...any) {}
func (testNopLogger) Infof(string, ...any)  {}
func (testNopLogger) Warnf(string, ...any)  {}
func (testNopLogger) Errorf(string, ...any) {}

func (testNopLogger) CtxDebug(context.Context, string, ...any)  {}
func (testNopLogger) CtxInfo(context.Context, string, ...any)   {}
func (testNopLogger) CtxWarn(context.Context, string, ...any)   {}
func (testNopLogger) CtxError(context.Context, string, ...any)  {}
func (testNopLogger) CtxDebugf(context.Context, string, ...any) {}
func (testNopLogger) CtxInfof(context.Context, string, ...any)  {}
func (testNopLogger) CtxWarnf(context.Context, string, ...any)  {}
func (testNopLogger) CtxErrorf(context.Context, string, ...any) {}

func (testNopLogger) WithCtx(context.Context) logx.Logger { return testNopLogger{} }
func (testNopLogger) WithService(string) logx.Logger      { return testNopLogger{} }
func (testNopLogger) WithAttrs(...logx.Attr) logx.Logger  { return testNopLogger{} }
func (testNopLogger) WithGroup(string) logx.Logger        { return testNopLogger{} }
func (testNopLogger) SetLevel(logx.Level)                 {}

var _ logx.Logger = testNopLogger{}
