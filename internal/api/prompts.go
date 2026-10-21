package api

import (
	"context"
	"regexp"
	"strings"

	"github.com/liftedkilt/openchore/internal/store"
)

// 任务④「让用户知道为什么有这个信息」+ 任务③「任务 prompt 界面可配」：
// 每个文本 AI 任务（规划/总结/措施蒸馏/措施加强/Copilot 起草/错题脚手架）
// 的 prompt 模板存在 app_settings（prompt_planner 等），空值回落内置默认。
// 模板里 {{name}} 形式的占位符由各调用点替换；模板与默认完全相同的段落
// （占位符外的指令骨架）保持英文原文，家长改写时可以只动语气或加要求。

// promptTemplate reads one prompt setting, falling back to def when the
// setting is empty. The returned template still carries {{placeholders}}
// for the caller's data; a template with no placeholders is legal (the
// parent may want a fully fixed instruction).
func promptTemplate(ctx context.Context, s *store.Store, key, def string) string {
	if v, err := s.GetSetting(ctx, key); err == nil && strings.TrimSpace(v) != "" {
		return v
	}
	return def
}

// applyTemplate substitutes {{placeholders}} in a prompt template. Unknown
// placeholders are replaced with "" so a stale template can't leak raw
// {{...}} into the model's prompt. Keys are matched without braces.
func applyTemplate(tmpl string, values map[string]string) string {
	re := regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*\}\}`)
	return re.ReplaceAllStringFunc(tmpl, func(m string) string {
		name := strings.TrimSpace(m[2 : len(m)-2])
		if v, ok := values[name]; ok {
			return v
		}
		return ""
	})
}

// renderPrompt combines the two: read the setting, then substitute.
func renderPrompt(ctx context.Context, s *store.Store, key, def string, values map[string]string) string {
	return applyTemplate(promptTemplate(ctx, s, key, def), values)
}
