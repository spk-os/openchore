package api

import (
	"context"
	"database/sql"
	"testing"

	"github.com/liftedkilt/openchore/internal/llm"
	"github.com/liftedkilt/openchore/internal/store"
	"github.com/liftedkilt/openchore/migrations"

	"github.com/golang-migrate/migrate/v4"
	msqlite "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "modernc.org/sqlite"
)

// 任务④：prompt 模板体系的契约测试——占位符替换、未知占位符清空、设置
// 覆盖回落链。renderPrompt 未导出，所以测在包内（api 包），用迁移好的
// 内存 store 走真实 SetSetting→GetSetting 链。

func promptTestStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=on&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })

	driver, err := msqlite.WithInstance(db, &msqlite.Config{})
	if err != nil {
		t.Fatalf("migration driver: %v", err)
	}
	source, err := iofs.New(migrations.FS, ".")
	if err != nil {
		t.Fatalf("migration source: %v", err)
	}
	m, err := migrate.NewWithInstance("iofs", source, "sqlite", driver)
	if err != nil {
		t.Fatalf("migrator: %v", err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Fatalf("migrations: %v", err)
	}
	return store.New(db)
}

// TestPromptTemplateFallback：无设置时用默认模板，占位符替换。
func TestPromptTemplateFallback(t *testing.T) {
	s := promptTestStore(t)
	got := renderPrompt(context.Background(), s, "prompt_planner", "plan for {{kid}}", map[string]string{
		"kid": "小明",
	})
	if got != "plan for 小明" {
		t.Fatalf("fallback render = %q", got)
	}
}

// TestPromptTemplateSettingWins：写入设置后覆盖默认，未知占位符变空串。
func TestPromptTemplateSettingWins(t *testing.T) {
	s := promptTestStore(t)
	if err := s.SetSetting(context.Background(), "prompt_scaffold", "给 {{kid}} 讲 {{stage}} 阶，别提 {{missing}}"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	got := renderPrompt(context.Background(), s, "prompt_scaffold", "default", map[string]string{
		"kid": "小明", "stage": "3",
	})
	if got != "给 小明 讲 3 阶，别提 " {
		t.Fatalf("setting-overridden render = %q", got)
	}
}

// TestCopilotPromptBodyCarriesAnchors：内置 Copilot 模板带上家里的锚点
// （孩子、现有任务、奖励阶梯、家长原话），prompt_copilot 改写者有整段
// 默认可以参考。
func TestCopilotPromptBodyCarriesAnchors(t *testing.T) {
	body := llm.CopilotPromptBody(llm.CopilotContext{
		KidName:        "小言",
		ExistingChores: []string{"数学错题订正"},
		RewardLadder:   []string{"看电影 30分"},
	}, "下周每天背单词")
	for _, want := range []string{"小言", "数学错题订正", "看电影 30分", "下周每天背单词"} {
		if !contains(body, want) {
			t.Errorf("CopilotPromptBody missing %q", want)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
