package argusctx

import (
	"context"
	"fmt"
	"strconv"

	"github.com/yi-nology/go-git-platform/provider"
)

// ---------- CR / diff / issue / file ----------

// call 平台调用的单源样板（v3.64.0，此前 12 个方法各写一遍）：Provider 解析 →
// bound ctx（SDK 调用超时）→ 执行。
func call[T any](s *SDKCtx, ctx context.Context, platform string,
	fn func(ctx context.Context, p provider.Provider) (T, error)) (T, error) {
	p, err := s.Provider(platform)
	if err != nil {
		var zero T
		return zero, err
	}
	ctx, cancel := s.bound(ctx)
	defer cancel()
	return fn(ctx, p)
}

// callIface 带能力接口断言的调用：断言失败 = unsupportedErr（断言在 bound 之后，
// 与既有方法顺序一致）。
func callIface[I any, T any](s *SDKCtx, ctx context.Context, platform, iface string,
	fn func(ctx context.Context, cap I) (T, error)) (T, error) {
	return call(s, ctx, platform, func(ctx context.Context, p provider.Provider) (T, error) {
		cap, ok := p.(I)
		if !ok {
			var zero T
			return zero, unsupportedErr(platform, iface)
		}
		return fn(ctx, cap)
	})
}

// call2 call 的双返回值变体（如 ListCRs 的 (items, total)）。
func call2[T1, T2 any](s *SDKCtx, ctx context.Context, platform string,
	fn func(ctx context.Context, p provider.Provider) (T1, T2, error)) (T1, T2, error) {
	p, err := s.Provider(platform)
	if err != nil {
		var z1 T1
		var z2 T2
		return z1, z2, err
	}
	ctx, cancel := s.bound(ctx)
	defer cancel()
	return fn(ctx, p)
}

// GetCR 拉取 PR 详情。
func (s *SDKCtx) GetCR(ctx context.Context, platform, owner, repo, number string) (*provider.ChangeRequest, error) {
	return call(s, ctx, platform, func(ctx context.Context, p provider.Provider) (*provider.ChangeRequest, error) {
		return p.GetCR(ctx, owner, repo, number)
	})
}

// CreateCR 创建 PR/MR（v7.0 Verified Fix Loop 阶段 2：修复分支开 PR）。
// Provider 接口内嵌 ChangeRequestManager（全平台必有），直连 call 无需能力断言。
func (s *SDKCtx) CreateCR(ctx context.Context, platform string, opts provider.CreateCROptions) (*provider.ChangeRequest, error) {
	return call(s, ctx, platform, func(ctx context.Context, p provider.Provider) (*provider.ChangeRequest, error) {
		return p.CreateCR(ctx, opts)
	})
}

// GetCRDiff 拉取 PR diff（结构化文件 + 原始 unified diff）。
func (s *SDKCtx) GetCRDiff(ctx context.Context, platform, owner, repo, number string) (*provider.MergeDiff, error) {
	return call(s, ctx, platform, func(ctx context.Context, p provider.Provider) (*provider.MergeDiff, error) {
		return p.GetCRDiff(ctx, owner, repo, number)
	})
}

// ListCRs 列出仓库 PR（轮询兜底任务源用）。
// v0.94.0 起 SDK total 语义显式化（*int：nil=平台未提供权威总数）——
// 本包装保持 int 返回面不变（消费方仅 poller 且丢弃 total），nil 归 0。
func (s *SDKCtx) ListCRs(ctx context.Context, platform string, opts provider.ListCROptions) ([]*provider.ChangeRequest, int, error) {
	items, total, err := call2(s, ctx, platform, func(ctx context.Context, p provider.Provider) ([]*provider.ChangeRequest, *int, error) {
		return p.ListCRs(ctx, opts)
	})
	if total == nil {
		return items, 0, err
	}
	return items, *total, err
}

// GetRepo 拉取仓库信息（默认分支）。
func (s *SDKCtx) GetRepo(ctx context.Context, platform, owner, repo string) (*provider.PlatformRepo, error) {
	return call(s, ctx, platform, func(ctx context.Context, p provider.Provider) (*provider.PlatformRepo, error) {
		return p.GetRepo(ctx, owner, repo)
	})
}

// unsupportedErr 平台不支持某接口的统一错误。
func unsupportedErr(platform, iface string) error {
	return fmt.Errorf("sdkctx: 平台 %s 不支持 %s", platform, iface)
}

// GetFileContent 读仓库文件（ref=分支/tag/SHA）。仓库配置只从默认分支读（§3.4 红线 1）。
func (s *SDKCtx) GetFileContent(ctx context.Context, platform, owner, repo, path, ref string) (string, error) {
	return callIface(s, ctx, platform, "FileManager",
		func(ctx context.Context, fm provider.FileManager) (string, error) {
			return fm.GetFileContent(ctx, owner, repo, path, ref)
		})
}

// GetIssue 读 issue（需求源）。
func (s *SDKCtx) GetIssue(ctx context.Context, platform, owner, repo, number string) (*provider.Issue, error) {
	return callIface(s, ctx, platform, "IssueManager",
		func(ctx context.Context, im provider.IssueManager) (*provider.Issue, error) {
			return im.GetIssue(ctx, owner, repo, number)
		})
}

// ListIssueComments 列 issue/PR 评论。v3.92.0：NoteManager（MR Notes API）
// 优先——GitLab 的 MR 与 issue 是两个 iid 命名空间，IssueManager 的评论方法
// 对 MR 必 404；GitHub/Gitea 等 issue 即 PR 的平台无 NoteManager，走原路。
func (s *SDKCtx) ListIssueComments(ctx context.Context, platform, owner, repo, number string) ([]*provider.IssueComment, error) {
	if c, err := callIface(s, ctx, platform, "NoteManager",
		func(ctx context.Context, nm provider.NoteManager) ([]*provider.IssueComment, error) {
			return nm.ListNotes(ctx, owner, repo, number)
		}); err == nil {
		return c, nil
	}
	return callIface(s, ctx, platform, "IssueManager",
		func(ctx context.Context, im provider.IssueManager) ([]*provider.IssueComment, error) {
			return im.ListIssueComments(ctx, owner, repo, number)
		})
}

// CreateIssueComment 发总结评论。
// 优先使用 DiffManager.CreateNote（MR Notes API），因为 Argus 任务对象全部是 CR/PR，
// GitLab 的 IssueManager.CreateIssueComment 走 Issues Notes API 会 404。
// 回退到 IssueManager 仅当平台不实现 DiffManager（如纯 Issue 场景）。
func (s *SDKCtx) CreateIssueComment(ctx context.Context, platform, owner, repo, number, body string) (*provider.IssueComment, error) {
	body = maskOutboundPII(body) // v6.16.0 出站值面 PII 脱敏（choke point 单点）
	return call(s, ctx, platform, func(ctx context.Context, p provider.Provider) (*provider.IssueComment, error) {
		// 优先走 DiffManager.CreateNote（MR Notes API），兼容所有 CR 场景。
		if dm, ok := p.(provider.DiffManager); ok {
			noteID, err := dm.CreateNote(ctx, owner, repo, number, body)
			if err == nil {
				// 解析失败留痕（深挖修复）：Comment.ID=0 会破坏按评论 ID 幂等更新的锚点
				id, perr := strconv.ParseInt(noteID, 10, 64)
				if perr != nil {
					s.log.Warn("argus.sdkctx.note_id_unparsable", "platform", platform,
						"note_id", noteID, "error", perr.Error())
				}
				return &provider.IssueComment{ID: id, Body: body}, nil
			}
			s.log.Warn("argus.sdkctx.create_note_fallback", "platform", platform, "error", err.Error())
		}
		// 回退到 IssueManager（Issue 评论 API）
		im, ok := p.(provider.IssueManager)
		if !ok {
			return nil, fmt.Errorf("sdkctx: 平台 %s 不支持 DiffManager 也不支持 IssueManager", platform)
		}
		return im.CreateIssueComment(ctx, owner, repo, number, body)
	})
}

// AddIssueCommentReaction 给评论加表情回应（v5.3.0 即时回执 👀）。
// 能力可选（ReactionManager）：不支持的平台返回 unsupported，调用方 best-effort 吞掉。
func (s *SDKCtx) AddIssueCommentReaction(ctx context.Context, platform, owner, repo string,
	commentID int64, emoji string) error {
	_, err := callIface(s, ctx, platform, "ReactionManager",
		func(ctx context.Context, rm provider.ReactionManager) (*provider.Reaction, error) {
			return rm.AddIssueCommentReaction(ctx, owner, repo, commentID, emoji)
		})
	return err
}

// ListCollaborators 列出仓库协作者（v5.3.0 评论指令权限门控）。
// 能力可选（CollaboratorManager）：不支持的平台返回 unsupported，门控侧 fail-open
// 并留痕（平台差异不能把评论链路整个堵死）。
func (s *SDKCtx) ListCollaborators(ctx context.Context, platform, owner, repo string) ([]*provider.Collaborator, error) {
	return callIface(s, ctx, platform, "CollaboratorManager",
		func(ctx context.Context, cm provider.CollaboratorManager) ([]*provider.Collaborator, error) {
			return cm.ListCollaborators(ctx, owner, repo)
		})
}

// UpdateIssueComment 更新总结评论。v3.92.0 路由序：NoteManager（MR Notes
// API，GitLab 真·原地更新）→ IssueManager（GitHub/Gitea 等 issue 即 PR 的
// 平台）→ v3.91.2 的 DiffManager 原子换新兜底（先 CreateNote 后 DeleteNote，
// 返回新 ID，调用方经 repointIfSwapped 回写缓存）。
func (s *SDKCtx) UpdateIssueComment(ctx context.Context, platform, owner, repo, number string, commentID int64, body string) (*provider.IssueComment, error) {
	body = maskOutboundPII(body) // v6.16.0 出站值面 PII 脱敏（choke point 单点）
	if c, err := callIface(s, ctx, platform, "NoteManager",
		func(ctx context.Context, nm provider.NoteManager) (*provider.IssueComment, error) {
			return nm.UpdateNote(ctx, owner, repo, number, strconv.FormatInt(commentID, 10), body)
		}); err == nil {
		return c, nil
	}
	c, err := callIface(s, ctx, platform, "IssueManager",
		func(ctx context.Context, im provider.IssueManager) (*provider.IssueComment, error) {
			return im.UpdateIssueComment(ctx, owner, repo, number, commentID, body)
		})
	if err == nil {
		return c, nil
	}
	fallback, ferr := call(s, ctx, platform, func(ctx context.Context, p provider.Provider) (*provider.IssueComment, error) {
		dm, ok := p.(provider.DiffManager)
		if !ok {
			return nil, err
		}
		nid, cerr := dm.CreateNote(ctx, owner, repo, number, body)
		if cerr != nil {
			s.log.Warn("argus.sdkctx.update_swap_create_failed", "platform", platform, "error", cerr.Error())
			return nil, err
		}
		newID, perr := strconv.ParseInt(nid, 10, 64)
		if perr != nil {
			// 新评论已建但 ID 不可解析：删旧保新（缓存定位失效属可容忍降级）
			s.log.Warn("argus.sdkctx.note_id_unparsable", "platform", platform, "note_id", nid, "error", perr.Error())
			newID = 0
		}
		if derr := dm.DeleteNote(ctx, owner, repo, number, strconv.FormatInt(commentID, 10)); derr != nil {
			s.log.Warn("argus.sdkctx.update_swap_delete_failed", "platform", platform,
				"comment_id", commentID, "error", derr.Error())
		}
		return &provider.IssueComment{ID: newID, Body: body}, nil
	})
	if ferr == nil {
		return fallback, nil
	}
	return c, err
}

// CreateReview 提交行内评论 review。
func (s *SDKCtx) CreateReview(ctx context.Context, platform, owner, repo, number string, opts provider.CreateReviewOptions) (*provider.ReviewResult, error) {
	opts = maskReviewOptions(opts) // v6.16.0 出站值面 PII 脱敏（总评+行内全包）
	return callIface(s, ctx, platform, "ReviewManager",
		func(ctx context.Context, rm provider.ReviewManager) (*provider.ReviewResult, error) {
			return rm.CreateReview(ctx, owner, repo, number, opts)
		})
}

// ListReviews 列出 PR 的评审记录（poster 评审幂等锚点用）。
func (s *SDKCtx) ListReviews(ctx context.Context, platform, owner, repo, number string) ([]provider.Review, error) {
	return callIface(s, ctx, platform, "ReviewManager",
		func(ctx context.Context, rm provider.ReviewManager) ([]provider.Review, error) {
			return rm.ListReviews(ctx, owner, repo, number)
		})
}

// DeleteIssueComment 删除一条评论（v5.4.7 总结评论轮换浮顶的删除面）。
// 走 DiffManager.DeleteNote（GitLab MR Notes API；UpdateIssueComment 的
// v3.92.0 换新兜底已实弹该面；provider.IssueManager 接口无删除方法）。
// 平台未实现 DiffManager 时报错，由调用方降级原地更新（不阻断回帖主链）。
func (s *SDKCtx) DeleteIssueComment(ctx context.Context, platform, owner, repo, number string, commentID int64) error {
	// 主路：IssueManager.DeleteIssueComment（gp#33 → v0.114.0 收编；GitLab/腾讯
	// 工蜂 route through issue，七平台全覆盖）；DiffManager.DeleteNote 兜底保留
	// （旧版本地板兼容，argus go.mod 已 v0.114 主路恒命中）。
	if _, err := callIface(s, ctx, platform, "IssueManager",
		func(ctx context.Context, im provider.IssueManager) (bool, error) {
			return true, im.DeleteIssueComment(ctx, owner, repo, number, commentID)
		}); err == nil {
		return nil
	}
	_, err := call(s, ctx, platform, func(ctx context.Context, p provider.Provider) (bool, error) {
		dm, ok := p.(provider.DiffManager)
		if !ok {
			return false, fmt.Errorf("platform %s: 无评论删除面（IssueManager/DiffManager 均未实现）", platform)
		}
		if derr := dm.DeleteNote(ctx, owner, repo, number, strconv.FormatInt(commentID, 10)); derr != nil {
			return false, derr
		}
		return true, nil
	})
	return err
}
