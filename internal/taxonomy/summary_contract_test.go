package taxonomy

import (
	"encoding/json"
	"testing"
)

/* Summary JSON 契约：snake_case tag + 所有 slice 非 nil（序列化为 [] 而非 null）。
   覆盖空结果（roots/path 为空）与有数据两条路径。 */

// marshalSummary 断言 Summary 可序列化且所有 slice 字段非 null。
func marshalSummary(t *testing.T, sum *Summary) map[string]json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(sum)
	if err != nil {
		t.Fatalf("序列化 Summary: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("解析 Summary JSON: %v（原文 %s）", err, raw)
	}
	for _, key := range []string{
		"targets", "mastered", "shaky", "weak", "unconfirmed", "untested", "roots", "path",
	} {
		v, ok := m[key]
		if !ok {
			t.Fatalf("Summary JSON 缺少字段 %s（原文 %s）", key, raw)
		}
		if string(v) == "null" {
			t.Errorf("Summary.%s 序列化为 null，期望 []（原文 %s）", key, raw)
		}
	}
	return m
}

func TestSummaryJSONContractAllSlicesNonNilWhenEmpty(t *testing.T) {
	c := smallCatalog()
	// 全部答对：roots/path/mastered 之外的状态列表为空，是 nil 的最坏情形。
	s, _ := runScript(t, c, mustSession(t, c, []string{"T"}, nil),
		map[string]string{"T": ResultCorrect})
	sum := c.Summarize(s)
	if sum == nil {
		t.Fatal("Summarize 返回 nil")
	}
	m := marshalSummary(t, sum)
	if string(m["roots"]) != "[]" {
		t.Errorf("roots = %s，期望 []", m["roots"])
	}
	if string(m["path"]) != "[]" {
		t.Errorf("path = %s，期望 []", m["path"])
	}
	// roots 内三项在无未测先修时也应为 []
	var roots []map[string]json.RawMessage
	if err := json.Unmarshal(m["roots"], &roots); err != nil {
		t.Fatalf("解析 roots: %v", err)
	}
	if len(roots) != 0 {
		t.Fatalf("roots 数量 = %d，期望 0", len(roots))
	}
}

func TestSummaryJSONContractKeysAndRootSlices(t *testing.T) {
	c := smallCatalog()
	// 制造根因 + 未测先修：T 答错、D 答错后停（limit=2），D 的先修 F 未测。
	s, _ := runScript(t, c, mustSession(t, c, []string{"T"}, &CreateSessionOptions{Limit: 2}),
		map[string]string{"T": ResultWrong, "D": ResultWrong})
	sum := c.Summarize(s)
	if len(sum.Roots) != 1 {
		t.Fatalf("roots 数量 = %d，期望 1", len(sum.Roots))
	}
	if len(sum.Path) == 0 {
		t.Fatal("path 不应为空")
	}
	// Go 字段层：roots 内三项非 nil
	r := sum.Roots[0]
	if r.PendingPrereqs == nil || r.OutOfScopePrereqs == nil || r.ExternalPrereqs == nil {
		t.Errorf("roots[0] 三项 prereq slice 不应为 nil：%+v", r)
	}
	m := marshalSummary(t, sum)

	// 顶层键：limit_reached 为 snake_case 且值正确
	if _, ok := m["limit_reached"]; !ok {
		t.Fatalf("Summary JSON 缺少键 limit_reached（原文 %s）", m)
	}
	if string(m["limit_reached"]) != "true" {
		t.Errorf("limit_reached = %s，期望 true", m["limit_reached"])
	}
	var roots []map[string]json.RawMessage
	if err := json.Unmarshal(m["roots"], &roots); err != nil {
		t.Fatalf("解析 roots: %v", err)
	}
	for _, key := range []string{"pending_prereqs", "out_of_scope_prereqs", "external_prereqs"} {
		v, ok := roots[0][key]
		if !ok {
			t.Fatalf("roots[0] 缺少键 %s", key)
		}
		if string(v) == "null" {
			t.Errorf("roots[0].%s = null，期望 []", key)
		}
	}
	var path []map[string]json.RawMessage
	if err := json.Unmarshal(m["path"], &path); err != nil {
		t.Fatalf("解析 path: %v", err)
	}
	if len(path) == 0 {
		t.Fatal("path 不应为空")
	}
	for _, key := range []string{"is_root", "is_target"} {
		if _, ok := path[0][key]; !ok {
			t.Errorf("path[0] 缺少键 %s", key)
		}
	}
}
