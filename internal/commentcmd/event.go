// event.go —— 评论类 webhook 事件形态折叠（v5.4.4 自 service 迁入）：
// canonical comment.created 与旧词表 issue_comment/cr.note 折叠成归一视图。
package commentcmd

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/yi-nology/go-git-platform/provider"
)

// commentEventTypes 可路由为评论命令的事件词表。canonical 为
// provider.EventTypeComment+CommentActionCreated（"comment.created"，SDK 七后端
// 统一）；issue_comment 与 cr.note 为旧词表，保留兼容防 vendor 回退误判。
var commentEventTypes = map[string]bool{
	"issue_comment": true,
	"cr.note":       true,
	provider.EventTypeComment + provider.CommentActionCreated: true,
}

// Meta 评论负载的归一化视图（两种平台形状折叠后的评论要素）。
type Meta struct {
	Body     string
	PRNumber int
	// CommentID 评论 ID（v5.3.0 即时回执 👀 的落点）；0 = 负载未携带（跳过回执）。
	CommentID int64
}

// ParseCommentMeta 按事件形态解析评论要素。canonical 事件（comment.created）
// 的归一化结构已就绪（Comment=评论体、CR=所在 MR），直接取用不重解 RawPayload；
// 旧词表 cr.note → GitLab payload 形状；issue_comment 与 Gitea/Forgejo（Type 空
// + comment 字段兜底）→ issue payload 形状。ok=false 表示解析失败或非 PR 评论
// （commit/snippet/普通 issue），调用方静默忽略。
func ParseCommentMeta(ev *provider.NormalizedEvent) (Meta, bool) {
	if ev.Type == provider.EventTypeComment+provider.CommentActionCreated {
		if ev.Comment == nil || ev.CR == nil {
			return Meta{}, false
		}
		n, err := strconv.Atoi(ev.CR.Number)
		if err != nil || n <= 0 {
			return Meta{}, false
		}
		return Meta{Body: ev.Comment.Body, PRNumber: n, CommentID: ev.Comment.ID}, true
	}
	if ev.Type == "cr.note" {
		var gl gitlabNotePayload
		if err := json.Unmarshal(ev.RawPayload, &gl); err != nil {
			return Meta{}, false
		}
		if gl.ObjectAttributes.System || gl.MergeRequest.IID == 0 {
			return Meta{}, false
		}
		return Meta{Body: gl.ObjectAttributes.Note, PRNumber: gl.MergeRequest.IID,
			CommentID: gl.ObjectAttributes.ID}, true
	}
	var pl qaCommentPayload
	if err := json.Unmarshal(ev.RawPayload, &pl); err != nil {
		return Meta{}, false
	}
	if len(pl.Issue.PullRequest) == 0 || string(pl.Issue.PullRequest) == "null" {
		return Meta{}, false
	}
	return Meta{Body: pl.Comment.Body, PRNumber: pl.Issue.Number, CommentID: pl.Comment.ID}, true
}

// qaCommentPayload 解析 issue_comment webhook 原始负载中我们关心的字段。
// 仅用于从 RawPayload 提取评论正文与 PR 信息，不做全量反序列化。
type qaCommentPayload struct {
	Comment struct {
		Body string `json:"body"`
		ID   int64  `json:"id"`
	} `json:"comment"`
	Issue struct {
		Number int `json:"number"`
		// GitHub 与 Gitea/Forgejo 的 pull_request 元数据字段集不完全一致，
		// 只取原始 JSON 判存在性（非空 = 这是 PR，不是普通 issue）。
		PullRequest json.RawMessage `json:"pull_request"`
	} `json:"issue"`
}

// gitlabNotePayload GitLab note webhook 负载（object_kind=note）中我们关心的字段。
// MR 评论带 merge_request 对象（iid 即 MR 序号）；system=true 是平台自动生成的
// 系统注释（改指派/标签/状态等），没有用户意图，必须跳过。
type gitlabNotePayload struct {
	ObjectAttributes struct {
		Note   string `json:"note"`
		System bool   `json:"system"`
		ID     int64  `json:"id"`
	} `json:"object_attributes"`
	MergeRequest struct {
		IID int `json:"iid"`
	} `json:"merge_request"`
}

// IsCommentEvent 判定 NormalizedEvent 是否为可处理的评论事件。
// canonical：comment.created（SDK 七后端统一，Actor/Repo/Comment/CR 已由 SDK
// 归一化填充）；旧词表 issue_comment（GitHub）/cr.note（GitLab）保留兼容；
// Gitea/Forgejo 未识别事件 → ev.Type 为空但 payload 含 comment 字段。
func IsCommentEvent(ev *provider.NormalizedEvent) bool {
	if commentEventTypes[ev.Type] {
		return true
	}
	if ev.Type != "" {
		return false
	}
	// Gitea 后端不识别的事件类型 → Type 为空；用 payload 结构兜底判定。
	var probe struct {
		Comment *json.RawMessage `json:"comment"`
	}
	return len(ev.RawPayload) > 0 &&
		json.Unmarshal(ev.RawPayload, &probe) == nil &&
		probe.Comment != nil
}

// NormalizeCommentEvent 从原始负载补齐评论事件的 Actor/Repo。
// 背景：SDK 的 GitHub 后端对 issue_comment 走 default 分支只置 Type，不填
// Actor/Repo（真机实测发现，评论链路因此静默早退）；Gitea/GitLab 后端则在
// switch 前统一填充。此处以负载为准兜底，使评论处理与 SDK 归一化程度解耦。
func NormalizeCommentEvent(ev *provider.NormalizedEvent) {
	if len(ev.RawPayload) == 0 {
		return
	}
	if ev.Repo == nil {
		var probe struct {
			Repository struct {
				FullName string `json:"full_name"`
				Name     string `json:"name"`
				Owner    struct {
					Login string `json:"login"`
				} `json:"owner"`
			} `json:"repository"`
		}
		if err := json.Unmarshal(ev.RawPayload, &probe); err == nil {
			owner, name := probe.Repository.Owner.Login, probe.Repository.Name
			if owner == "" || name == "" {
				if i := strings.IndexByte(probe.Repository.FullName, '/'); i > 0 {
					if owner == "" {
						owner = probe.Repository.FullName[:i]
					}
					if name == "" {
						name = probe.Repository.FullName[i+1:]
					}
				}
			}
			if owner != "" && name != "" {
				ev.Repo = &provider.EventRepo{Owner: owner, Name: name}
			}
		}
	}
	if ev.Actor == nil {
		var probe struct {
			Sender struct {
				Login string `json:"login"`
			} `json:"sender"`
			User struct {
				Username string `json:"username"`
				Login    string `json:"login"`
			} `json:"user"`
		}
		if err := json.Unmarshal(ev.RawPayload, &probe); err == nil {
			login := probe.Sender.Login
			if login == "" {
				login = probe.User.Login
			}
			if login == "" {
				login = probe.User.Username
			}
			if login != "" {
				ev.Actor = &provider.CRUser{Username: login}
			}
		}
	}
}
