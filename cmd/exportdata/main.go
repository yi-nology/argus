// exportdata —— G1c 训练数据导出管道（v3.13.0，MiMo 差距批次 4 的可执行前置）。
//
// 把学习环积累的原料（pattern_signals 的 posted/regret/suppress/restore 计数、
// finding_prints 的跨轮复现与人工 suppress 结论）导出为 JSONL，供后续审查域
// 微调/蒸馏与离线分析使用。只读导出，不写任何业务表。
//
// 用法：
//
//	go run ./cmd/exportdata -dsn "file:argus.db" -out training.jsonl
//
// 数据源默认取 ARGUS_DB_DSN（与主进程同库；lacus/.env 自动加载）。
// 每行一个 JSON 对象：
//
//	{"type":"pattern", "platform":..,"owner":..,"repo":..,"plugin":..,
//	   "sample":..,"posted":N,"regret":N,"suppress":N,"restore":N,"confidence":0.xx}
//	{"type":"print",  "platform":..,"owner":..,"repo":..,"number":..,"plugin":..,
//	   "file":..,"line":N,"severity":..,"comment":..,"status":"open|suppressed",
//	   "times_seen":N,"regret_count":N}
//
// 软标签口径（下游训练自行取舍）：
//   - pattern.confidence 高（≥SoftFloor）= 可信审查意见族；低于 HardFloor = 误报族；
//   - print.status=suppressed = 人工确认误报（最强负标签）；regret_count ≥2 = 终审
//     剔除史（弱负标签）；times_seen ≥2 且 open = 跨轮复现的真问题（正样本）。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"git.example.com/enjoydream/ekit/observability/logx"

	"argus/internal/config"
	"argus/internal/plugin"
	"argus/internal/storage"
)

type exportRow struct {
	Type string `json:"type"`

	Platform string `json:"platform,omitempty"`
	Owner    string `json:"owner,omitempty"`
	Repo     string `json:"repo,omitempty"`

	// pattern 行
	Plugin     string   `json:"plugin,omitempty"`
	Sample     string   `json:"sample,omitempty"`
	Posted     int      `json:"posted,omitempty"`
	Regret     int      `json:"regret,omitempty"`
	Suppress   int      `json:"suppress,omitempty"`
	Restore    int      `json:"restore,omitempty"`
	Confidence *float64 `json:"confidence,omitempty"`

	// print 行
	Number    string `json:"number,omitempty"`
	File      string `json:"file,omitempty"`
	Line      int    `json:"line,omitempty"`
	Severity  string `json:"severity,omitempty"`
	Comment   string `json:"comment,omitempty"`
	Status    string `json:"status,omitempty"`
	TimesSeen int    `json:"times_seen,omitempty"`
	RegretCnt int    `json:"regret_count,omitempty"`
}

func main() {
	out := flag.String("out", "training-data.jsonl", "输出 JSONL 路径")
	dsnFlag := flag.String("dsn", "", "DB DSN（缺省取 ARGUS_DB_DSN）")
	timeout := flag.Duration("timeout", 5*time.Minute, "导出总时长上限")
	flag.Parse()

	config.LoadDotEnv()
	dsn := *dsnFlag
	if dsn == "" {
		dsn = os.Getenv("ARGUS_DB_DSN")
	}
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "exportdata: 需要 -dsn 或 ARGUS_DB_DSN")
		os.Exit(2)
	}
	log := logx.NewSlogLogger("argus-export")
	store, err := storage.Open(dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "exportdata: 打开 %s 失败: %v\n", dsn, err)
		os.Exit(1)
	}
	defer func() { _ = store.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	patterns, err := store.ListAllPatternSignals(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "exportdata: 读 pattern_signals 失败: %v\n", err)
		os.Exit(1)
	}
	prints, err := store.ListAllPrints(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "exportdata: 读 finding_prints 失败: %v\n", err)
		os.Exit(1)
	}

	f, err := os.Create(*out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "exportdata: 创建 %s 失败: %v\n", *out, err)
		os.Exit(1)
	}
	defer func() { _ = f.Close() }()
	enc := json.NewEncoder(f)

	rows := 0
	for _, p := range patterns {
		sample := p.PatternText
		if sample == "" {
			sample = p.Pattern
		}
		conf := plugin.Confidence(p.Posted, p.Regret, p.Suppress, p.Restore)
		if err := enc.Encode(exportRow{Type: "pattern", Platform: p.Platform, Owner: p.Owner,
			Repo: p.Repo, Plugin: p.Plugin, Sample: sample, Posted: p.Posted, Regret: p.Regret,
			Suppress: p.Suppress, Restore: p.Restore, Confidence: &conf}); err != nil {
			fmt.Fprintf(os.Stderr, "exportdata: 写入失败: %v\n", err)
			os.Exit(1)
		}
		rows++
	}
	for _, pr := range prints {
		if err := enc.Encode(exportRow{Type: "print", Platform: pr.Platform, Owner: pr.Owner,
			Repo: pr.Repo, Number: pr.Number, Plugin: pr.Plugin, File: pr.File, Line: pr.Line,
			Severity: pr.Severity, Comment: pr.Comment, Status: pr.Status,
			TimesSeen: pr.TimesSeen, RegretCnt: pr.RegretCount}); err != nil {
			fmt.Fprintf(os.Stderr, "exportdata: 写入失败: %v\n", err)
			os.Exit(1)
		}
		rows++
	}
	log.Info("argus.export.done", "out", *out, "rows", rows,
		"patterns", len(patterns), "prints", len(prints))
	fmt.Printf("导出完成：%d 行（patterns=%d, prints=%d）→ %s\n", rows, len(patterns), len(prints), *out)
}
