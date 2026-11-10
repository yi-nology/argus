package storage

import (
	"context"
	"testing"
	"time"

	"github.com/yi-nology/argus/internal/reposettings"
)

func TestRepoSettingCRUD(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// 无配置 = 默认态（nil, nil）。
	got, err := s.GetRepoSetting(ctx, "gitea", "acme", "web")
	if err != nil || got != nil {
		t.Fatalf("默认态应为 (nil,nil)，得到 (%v, %v)", got, err)
	}

	// upsert 创建。
	set := func(body string) {
		t.Helper()
		rs, perr := reposettings.Parse([]byte(body))
		if perr != nil {
			t.Fatalf("Parse: %v", perr)
		}
		norm, nerr := rs.Normalize()
		if nerr != nil {
			t.Fatalf("Normalize: %v", nerr)
		}
		if uerr := s.UpsertRepoSetting(ctx, &RepoSetting{
			Platform: "gitea", Owner: "acme", Repo: "web",
			SettingsJSON: string(norm), UpdatedBy: "fp01234567",
			UpdatedAt: time.Now(),
		}); uerr != nil {
			t.Fatalf("Upsert: %v", uerr)
		}
	}
	set(`{"monthly_token_budget": 5000000, "max_diff_chars": 200000}`)

	got, err = s.GetRepoSetting(ctx, "gitea", "acme", "web")
	if err != nil || got == nil {
		t.Fatalf("GetRepoSetting: (%v, %v)", got, err)
	}
	parsed, perr := reposettings.Parse([]byte(got.SettingsJSON))
	if perr != nil {
		t.Fatalf("落库 JSON 不可解析: %v", perr)
	}
	if parsed.MonthlyTokenBudgetOr(0) != 5_000_000 || parsed.MaxDiffCharsOr(0) != 200_000 {
		t.Fatalf("落库值不符: %+v", parsed)
	}
	if got.UpdatedBy != "fp01234567" {
		t.Fatalf("updated_by = %q", got.UpdatedBy)
	}

	// upsert 覆盖（同键更新，不新增行）。
	set(`{"monthly_token_budget": 9000000}`)
	got, err = s.GetRepoSetting(ctx, "gitea", "acme", "web")
	if err != nil || got == nil {
		t.Fatalf("覆盖后 Get: (%v, %v)", got, err)
	}
	parsed, _ = reposettings.Parse([]byte(got.SettingsJSON))
	if parsed.MonthlyTokenBudgetOr(0) != 9_000_000 || parsed.MaxDiffChars != nil {
		t.Fatalf("覆盖未收敛: %+v", parsed)
	}
	all, lerr := s.ListRepoSettings(ctx)
	if lerr != nil || len(all) != 1 {
		t.Fatalf("List 应恰 1 行: (%v, %v)", all, lerr)
	}

	// 主键点查不串仓。
	other, gerr := s.GetRepoSetting(ctx, "gitea", "acme", "api")
	if gerr != nil || other != nil {
		t.Fatalf("他仓应为默认态: (%v, %v)", other, gerr)
	}

	// 删除幂等：首次 true，二次 false。
	deleted, derr := s.DeleteRepoSetting(ctx, "gitea", "acme", "web")
	if derr != nil || !deleted {
		t.Fatalf("首删应 true: (%v, %v)", deleted, derr)
	}
	deleted, derr = s.DeleteRepoSetting(ctx, "gitea", "acme", "web")
	if derr != nil || deleted {
		t.Fatalf("二删应 false: (%v, %v)", deleted, derr)
	}
	got, err = s.GetRepoSetting(ctx, "gitea", "acme", "web")
	if err != nil || got != nil {
		t.Fatalf("删后应回默认态: (%v, %v)", got, err)
	}
}

func TestRepoSettingListOrder(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	for _, k := range [][3]string{
		{"gitlab", "acme", "api"}, {"gitea", "acme", "web"}, {"gitea", "acme", "api"},
	} {
		if uerr := s.UpsertRepoSetting(ctx, &RepoSetting{
			Platform: k[0], Owner: k[1], Repo: k[2],
			SettingsJSON: `{"max_diff_chars":1000}`, UpdatedAt: time.Now(),
		}); uerr != nil {
			t.Fatalf("Upsert %v: %v", k, uerr)
		}
	}
	all, err := s.ListRepoSettings(ctx)
	if err != nil || len(all) != 3 {
		t.Fatalf("List: (%v, %v)", all, err)
	}
	want := [][2]string{{"gitea", "api"}, {"gitea", "web"}, {"gitlab", "api"}}
	for i, w := range want {
		if all[i].Platform != w[0] || all[i].Repo != w[1] {
			t.Fatalf("第 %d 行排序错误：want %v got %s/%s", i, w, all[i].Platform, all[i].Repo)
		}
	}
}

func TestRepoSettingKeyValidation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.GetRepoSetting(ctx, "gitea", "", "web"); err == nil {
		t.Fatal("缺 owner 应报错")
	}
	if _, err := s.DeleteRepoSetting(ctx, "gitea", "acme", ""); err == nil {
		t.Fatal("缺 repo 应报错")
	}
	if err := s.UpsertRepoSetting(ctx, &RepoSetting{SettingsJSON: "{}"}); err == nil {
		t.Fatal("缺主键应报错")
	}
}
