package service

import "testing"

// v5.2.0 评论指令解析面：mention 任意位置 + 显式重审命令 + 单条复核命令。
// 多平台（GitHub/GitLab/Gitea）评论正文同为纯文本，解析面平台无关；平台差异
// 在 parseCommentMeta 层已另有测试钉住。

// mention 任意位置触发（句首/句中/句尾），词边界与引用行守卫，多 mention 取首个后文。
func TestExtractQuestion_MentionAnywhere(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"句首命令", "@argus-bot 评审一下", "评审一下"},
		{"句中 mention", "请 @argus-bot 复核 fp:ab12cd34", "复核 fp:ab12cd34"},
		{"句尾 mention 取去 mention 全文", "帮我看看这个 @argus-bot", "帮我看看这个"},
		{"ask 前缀剥离", "@argus-bot ask 这个改动为什么这么写", "这个改动为什么这么写"},
		{"句中 ask", "麻烦 @argus-bot ask 说明一下", "说明一下"},
		{"大小写不敏感", "@Argus-Bot Review", "Review"},
		{"纯 mention 忽略", "@argus-bot", ""},
		{"纯 mention 带空白", "  @argus-bot  ", ""},
		{"无 mention 忽略", "这个改动 lgtm", ""},
		{"邮箱样式不触发", "联系 a@argus-bot.example 谢谢", ""},
		{"更长用户名不触发", "@argus-bot2 评审一下", ""},
		{"引用行 mention 跳过", "> @argus-bot 请重新审查\n\n同问", ""}, // 无有效 mention：引用是转述，整条忽略
		{"引用行跳过取后续 mention", "> @argus-bot 请重新审查\n@argus-bot 我的问题是为什么", "我的问题是为什么"},
		{"多 mention 取首个后文", "@argus-bot 帮忙看看 @alice 的意见", "帮忙看看 @alice 的意见"},
		{"mention 后换行指令", "@argus-bot\ndescribe", "describe"},
		{"空 body", "", ""},
		{"空 bot 名", "@argus x", ""},
	}
	for _, c := range cases {
		bot := "argus-bot"
		if c.name == "空 bot 名" {
			bot = ""
		}
		if got := extractQuestion(c.in, bot); got != c.want {
			t.Errorf("%s: extractQuestion(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// 显式重审命令：短指令确定性命中；词边界不吞长词；fp: 形态让位单条复核。
func TestExtractReviewCmd(t *testing.T) {
	cases := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"review", "", true},
		{"review 重点看登录模块", "重点看登录模块", true},
		{"评审一下", "一下", true},
		{"评审", "", true},
		{"复核一下这个 PR", "一下这个 PR", true},
		{"重新审查", "", true},
		{"re-run", "", false}, // 词表无此项（rerun 才是命令词）
		{"rerun", "", true},
		{"re-review", "", true},
		{"reviewing this PR", "", false}, // 词边界：reviewing 不是 review
		{"reviewboard", "", false},
		{"recheck fp:ab12cd34", "", false}, // fp 形态归复核命令
		{"复核 fp:ab12cd34 这条", "", false},
		{"fix fp:ab12cd34", "", false},
		{"帮我评审一下", "", false}, // 非命令形态（关键词/意图路由处理）
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := extractReviewCmd(c.in)
		if ok != c.wantOK || got != c.want {
			t.Errorf("extractReviewCmd(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.wantOK)
		}
	}
}

// 单条复核命令：fp 合法/非法/非复核形态三分类；note 为短码后补充说明。
func TestParseRecheck(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantFP   string
		wantNote string
		wantKind recheckKind
	}{
		{"recheck 合法短码", "recheck fp:ab12cd34", "ab12cd34", "", recheckFP},
		{"复核带补充说明", "复核 fp:ab12cd34 这条是不是误报", "ab12cd34", "这条是不是误报", recheckFP},
		{"短码大小写归一", "recheck fp:AB12CD34", "ab12cd34", "", recheckFP},
		{"短码非法", "recheck fp:xyz", "", "", recheckFPBad},
		{"短码过短", "复核 fp:ab12", "", "", recheckFPBad},
		{"带 fp 但格式坏", "复核 fp: 什么", "", "", recheckFPBad},
		{"无 fp 交重审命令", "复核一下这个 PR", "", "", recheckNone},
		{"非复核命令", "review", "", "", recheckNone},
		{"fix 不吞", "fix fp:ab12cd34", "", "", recheckNone},
	}
	for _, c := range cases {
		fp, note, kind := parseRecheck(c.in)
		if fp != c.wantFP || note != c.wantNote || kind != c.wantKind {
			t.Errorf("%s: parseRecheck(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.name, c.in, fp, note, kind, c.wantFP, c.wantNote, c.wantKind)
		}
	}
}
