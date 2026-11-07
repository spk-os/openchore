package api_test

// TX-ERROR-TAGS-HTTP-TESTER：验证 PUT /api/admin/error-items/{id} 的
// knowledge_tag_ids 契约与原子回滚。走真实 NewRouter/httptest + 全量迁移的
// SQLite（单写者连接池），夹具只用真 store DAO 写入，不 mock、不绕过 HTTP。
// 复用包内既有 setupTest / adminHeaders / childHeaders / seedSubjects；
// 本文件所有 helper 统一加 errorTagsHTTP 前缀，避免与既有 helper 重名。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/liftedkilt/openchore/internal/model"
)

type errorTagsHTTPFixture struct {
	env   *testEnv
	kidID int64
}

// errorTagsHTTPSetup 每个顶层用例启动一次真 router + 内存库。切到临时目录，
// 避免 NewRouter 的 MkdirAll("data/uploads") 写进仓库；再种真学科词表，
// 因为 042 之后 validSubject 以 subjects 表为准。
func errorTagsHTTPSetup(t *testing.T) *errorTagsHTTPFixture {
	t.Helper()
	t.Chdir(t.TempDir())
	env := setupTest(t)
	env.createAdmin(t)
	kidID := int64(env.createChild(t, "TagKid"))
	seedSubjects(t, env)
	return &errorTagsHTTPFixture{env: env, kidID: kidID}
}

type errorTagsHTTPCase struct {
	itemID                                 int64
	path                                   string
	mathA, mathB, mathC, chineseA, unknown int64
	originalQuestion                       string
}

// errorTagsHTTPNewItem 为每个子测试造一条独立错题：subject=math、两个旧标签
// A/B，另备 math C、chinese A 和一个保证不存在的 id。子测试之间互不污染。
func errorTagsHTTPNewItem(t *testing.T, f *errorTagsHTTPFixture, label string) *errorTagsHTTPCase {
	t.Helper()
	ctx := t.Context()
	question := "原题：" + label
	item := &model.ErrorItem{
		UserID:           f.kidID,
		Subject:          "math",
		OriginalImageURL: "https://example.test/error-item-tags-http.png",
		QuestionText:     question,
		MistakeStatus:    model.MistakeWrongAttempt,
		MasteryLevel:     model.MasteryNew,
	}
	if err := f.env.store.CreateErrorItem(ctx, item); err != nil {
		t.Fatalf("CreateErrorItem(%s): %v", label, err)
	}
	newTag := func(name, subject string) int64 {
		t.Helper()
		id, err := f.env.store.FindOrCreateKnowledgeTag(ctx, "TX-"+label+"-"+name, subject, nil)
		if err != nil {
			t.Fatalf("FindOrCreateKnowledgeTag(%s/%s): %v", label, name, err)
		}
		return id
	}
	c := &errorTagsHTTPCase{
		itemID:           item.ID,
		path:             fmt.Sprintf("/api/admin/error-items/%d", item.ID),
		mathA:            newTag("math-a", "math"),
		mathB:            newTag("math-b", "math"),
		mathC:            newTag("math-c", "math"),
		chineseA:         newTag("chinese-a", "chinese"),
		originalQuestion: question,
	}
	var maxID int64
	if err := f.env.db.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM knowledge_tags`).Scan(&maxID); err != nil {
		t.Fatalf("查询最大 knowledge_tags.id: %v", err)
	}
	c.unknown = maxID + 1000
	if err := f.env.store.ReplaceErrorItemTags(ctx, c.itemID, []int64{c.mathA, c.mathB}); err != nil {
		t.Fatalf("预置旧标签链接: %v", err)
	}
	errorTagsHTTPAssertState(t, f.env, c, "math", c.originalQuestion, c.mathA, c.mathB)
	return c
}

// errorTagsHTTPRequest 走真实 HTTP 并读完整响应体；状态不符时把 body 一起打出，
// 便于定位 4xx/5xx 的具体错误。
func errorTagsHTTPRequest(t *testing.T, e *testEnv, method, path string, body any, headers map[string]string, want int) []byte {
	t.Helper()
	resp := e.request(t, method, path, body, headers)
	return errorTagsHTTPCheckResponse(t, method, path, resp, want)
}

// errorTagsHTTPRawRequest sends body verbatim (no json.Marshal) — needed for
// the >1MB payload case; every other case goes through errorTagsHTTPRequest.
func errorTagsHTTPRawRequest(t *testing.T, e *testEnv, method, path string, body []byte, headers map[string]string, want int) []byte {
	t.Helper()
	req, err := http.NewRequest(method, e.server.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build raw request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s (raw): %v", method, path, err)
	}
	return errorTagsHTTPCheckResponse(t, method, path, resp, want)
}

func errorTagsHTTPCheckResponse(t *testing.T, method, path string, resp *http.Response, want int) []byte {
	t.Helper()
	defer resp.Body.Close()
	// A 400 whose body did not hit MaxBytesReader leaves the unread JSON
	// on the wire and http.Client EOFs the next request's reused keep-alive
	// connection. The leftover is always small, so drain it (bounded).
	const maxDrain = 4 << 20
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxDrain))
	if err != nil {
		t.Fatalf("%s %s: 读取响应失败: %v", method, path, err)
	}
	if resp.StatusCode != want {
		t.Fatalf("%s %s: status = %d, want %d; body = %s", method, path,
			resp.StatusCode, want, strings.TrimSpace(string(raw)))
	}
	return raw
}

type errorTagsHTTPItem struct {
	ID           int64                `json:"id"`
	Subject      string               `json:"subject"`
	QuestionText string               `json:"question_text"`
	Tags         []model.KnowledgeTag `json:"tags"`
}

func errorTagsHTTPTagIDs(tags []model.KnowledgeTag) []int64 {
	ids := make([]int64, 0, len(tags))
	for _, tag := range tags {
		ids = append(ids, tag.ID)
	}
	slices.Sort(ids)
	return ids
}

func errorTagsHTTPWantIDs(ids ...int64) []int64 {
	want := append([]int64(nil), ids...)
	slices.Sort(want)
	return want
}

func errorTagsHTTPAssertIDs(t *testing.T, got, want []int64, what string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

func errorTagsHTTPDecode(t *testing.T, raw []byte, what string) (errorTagsHTTPItem, map[string]json.RawMessage) {
	t.Helper()
	var item errorTagsHTTPItem
	if err := json.Unmarshal(raw, &item); err != nil {
		t.Fatalf("%s: JSON 解析失败: %v; body = %s", what, err, raw)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("%s: JSON 字段解析失败: %v", what, err)
	}
	return item, fields
}

func errorTagsHTTPAssertErrorJSON(t *testing.T, raw []byte) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("错误响应不是 JSON: %v; body = %s", err, raw)
	}
	if msg, ok := m["error"].(string); !ok || msg == "" {
		t.Fatalf("错误响应缺少非空 error 字段: %s", raw)
	}
}

// errorTagsHTTPAssertPutResponse 校验 PUT 成功响应本身就是最新状态：
// 不能返回内存里尚未换标签的旧 item。清空时 model 的 omitempty 允许省略
// tags 键，但绝不允许残留元素；非空时必须真的带 tags 数组。
func errorTagsHTTPAssertPutResponse(t *testing.T, raw []byte, wantSubject, wantQuestion string, wantTags ...int64) {
	t.Helper()
	item, fields := errorTagsHTTPDecode(t, raw, "PUT 响应")
	if item.Subject != wantSubject || item.QuestionText != wantQuestion {
		t.Fatalf("PUT 响应 subject/question = %q/%q, want %q/%q",
			item.Subject, item.QuestionText, wantSubject, wantQuestion)
	}
	want := errorTagsHTTPWantIDs(wantTags...)
	errorTagsHTTPAssertIDs(t, errorTagsHTTPTagIDs(item.Tags), want, "PUT 响应 tags")
	if len(want) > 0 {
		v, ok := fields["tags"]
		if !ok || string(v) == "null" {
			t.Fatalf("PUT 响应应带非空 tags 数组, got %s", v)
		}
	}
}

// errorTagsHTTPAssertState 三路复核：重新 GET、真 DB 行/链接表、store 的
// TagsForItem。任何一路残留旧字段或旧标签都会被抓到。
func errorTagsHTTPAssertState(t *testing.T, e *testEnv, c *errorTagsHTTPCase, wantSubject, wantQuestion string, wantTags ...int64) {
	t.Helper()
	want := errorTagsHTTPWantIDs(wantTags...)

	raw := errorTagsHTTPRequest(t, e, http.MethodGet,
		fmt.Sprintf("/api/error-items/%d", c.itemID), nil, adminHeaders(), http.StatusOK)
	got, _ := errorTagsHTTPDecode(t, raw, "重新 GET 响应")
	if got.Subject != wantSubject || got.QuestionText != wantQuestion {
		t.Fatalf("GET subject/question = %q/%q, want %q/%q",
			got.Subject, got.QuestionText, wantSubject, wantQuestion)
	}
	errorTagsHTTPAssertIDs(t, errorTagsHTTPTagIDs(got.Tags), want, "GET 响应 tags")

	var dbSubject, dbQuestion string
	if err := e.db.QueryRow(`SELECT subject, question_text FROM error_items WHERE id = ?`, c.itemID).
		Scan(&dbSubject, &dbQuestion); err != nil {
		t.Fatalf("读取 error_items 行: %v", err)
	}
	if dbSubject != wantSubject || dbQuestion != wantQuestion {
		t.Fatalf("DB subject/question = %q/%q, want %q/%q",
			dbSubject, dbQuestion, wantSubject, wantQuestion)
	}
	rows, err := e.db.Query(`SELECT tag_id FROM error_item_tags WHERE error_item_id = ? ORDER BY tag_id`, c.itemID)
	if err != nil {
		t.Fatalf("读取 error_item_tags: %v", err)
	}
	dbIDs := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			t.Fatalf("扫描 error_item_tags: %v", err)
		}
		dbIDs = append(dbIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatalf("遍历 error_item_tags: %v", err)
	}
	rows.Close()
	errorTagsHTTPAssertIDs(t, dbIDs, want, "DB error_item_tags")

	tags, err := e.store.TagsForItem(t.Context(), c.itemID)
	if err != nil {
		t.Fatalf("TagsForItem: %v", err)
	}
	errorTagsHTTPAssertIDs(t, errorTagsHTTPTagIDs(tags), want, "TagsForItem")
}

func errorTagsHTTPAssertLinkCounts(t *testing.T, e *testEnv, itemID int64, want int) {
	t.Helper()
	var total, distinct int
	if err := e.db.QueryRow(
		`SELECT COUNT(*), COUNT(DISTINCT tag_id) FROM error_item_tags WHERE error_item_id = ?`, itemID).
		Scan(&total, &distinct); err != nil {
		t.Fatalf("统计 error_item_tags: %v", err)
	}
	if total != want || distinct != want {
		t.Fatalf("链接数 total=%d distinct=%d, want %d/%d（重复 ID 不得制造重复链接）",
			total, distinct, want, want)
	}
}

// 顶层一：权限与不存在项。PUT 仍只允许 admin，且 404 不留幽灵链接。
func TestUpdateErrorItemKnowledgeTagsAuthAndMissing(t *testing.T) {
	f := errorTagsHTTPSetup(t)
	c := errorTagsHTTPNewItem(t, f, "auth")

	t.Run("UnauthenticatedIs401", func(t *testing.T) {
		raw := errorTagsHTTPRequest(t, f.env, http.MethodPut, c.path,
			map[string]any{"knowledge_tag_ids": []int64{c.mathA}}, nil, http.StatusUnauthorized)
		errorTagsHTTPAssertErrorJSON(t, raw)
		errorTagsHTTPAssertState(t, f.env, c, "math", c.originalQuestion, c.mathA, c.mathB)
	})

	t.Run("ChildIs403", func(t *testing.T) {
		raw := errorTagsHTTPRequest(t, f.env, http.MethodPut, c.path,
			map[string]any{"knowledge_tag_ids": []int64{c.mathA}},
			childHeaders(int(f.kidID)), http.StatusForbidden)
		errorTagsHTTPAssertErrorJSON(t, raw)
		errorTagsHTTPAssertState(t, f.env, c, "math", c.originalQuestion, c.mathA, c.mathB)
	})

	t.Run("MissingItemIs404", func(t *testing.T) {
		missingID := c.itemID + 9999
		raw := errorTagsHTTPRequest(t, f.env, http.MethodPut,
			fmt.Sprintf("/api/admin/error-items/%d", missingID),
			map[string]any{"knowledge_tag_ids": []int64{c.mathA}},
			adminHeaders(), http.StatusNotFound)
		errorTagsHTTPAssertErrorJSON(t, raw)
		errorTagsHTTPAssertState(t, f.env, c, "math", c.originalQuestion, c.mathA, c.mathB)
		var n int
		if err := f.env.db.QueryRow(
			`SELECT COUNT(*) FROM error_item_tags WHERE error_item_id = ?`, missingID).Scan(&n); err != nil {
			t.Fatalf("统计幽灵链接: %v", err)
		}
		if n != 0 {
			t.Fatalf("不存在的错题留下 %d 条幽灵链接", n)
		}
	})
}

// 顶层二：各类非法标签必须 400，且同请求尝试改的 subject/question_text
// 与旧标签一起整单回滚。每个子测试用独立错题，避免相互污染。
func TestUpdateErrorItemKnowledgeTagsRejections(t *testing.T) {
	f := errorTagsHTTPSetup(t)

	assertRejected := func(t *testing.T, c *errorTagsHTTPCase, subject string, tagIDs any) {
		t.Helper()
		raw := errorTagsHTTPRequest(t, f.env, http.MethodPut, c.path,
			map[string]any{
				"subject":           subject,
				"question_text":     "被回滚的题干",
				"knowledge_tag_ids": tagIDs,
			}, adminHeaders(), http.StatusBadRequest)
		errorTagsHTTPAssertErrorJSON(t, raw)
		errorTagsHTTPAssertState(t, f.env, c, "math", c.originalQuestion, c.mathA, c.mathB)
	}

	t.Run("ZeroTagIDIs400AndRollsBack", func(t *testing.T) {
		assertRejected(t, errorTagsHTTPNewItem(t, f, "zero"), "chinese", []int64{0})
	})

	t.Run("NegativeTagIDIs400AndRollsBack", func(t *testing.T) {
		assertRejected(t, errorTagsHTTPNewItem(t, f, "negative"), "chinese", []int64{-1})
	})

	t.Run("NonArrayTagIDsIs400AndRollsBack", func(t *testing.T) {
		assertRejected(t, errorTagsHTTPNewItem(t, f, "non-array"), "chinese", "oops")
	})

	t.Run("UnknownTagIDIs400AndRollsBack", func(t *testing.T) {
		c := errorTagsHTTPNewItem(t, f, "unknown")
		assertRejected(t, c, "chinese", []int64{c.unknown})
	})

	// 最终 subject=english、标签属于 chinese：异学科标签拒绝。
	t.Run("CrossSubjectTagIs400AndRollsBack", func(t *testing.T) {
		c := errorTagsHTTPNewItem(t, f, "cross-subject")
		assertRejected(t, c, "english", []int64{c.chineseA})
	})

	// 同请求把 subject 改成 chinese，却仍提交 math 标签：必须按最终 subject
	// 校验并失败；若实现按旧 subject=math 校验就会错误地成功。
	t.Run("SubjectChangeWithMismatchedTagIs400AndRollsBack", func(t *testing.T) {
		c := errorTagsHTTPNewItem(t, f, "mismatched")
		assertRejected(t, c, "chinese", []int64{c.mathA})
	})
}

// 顶层三：成功的四种语义（缺省/null 保留、[] 清空、去重替换）与
// 「改 subject + 同学科标签」组合；每条都核对 PUT 响应、重新 GET、DB 三处一致。
func TestUpdateErrorItemKnowledgeTagsSuccesses(t *testing.T) {
	f := errorTagsHTTPSetup(t)

	assertSuccess := func(t *testing.T, c *errorTagsHTTPCase, body map[string]any,
		wantSubject, wantQuestion string, wantTags ...int64) {
		t.Helper()
		raw := errorTagsHTTPRequest(t, f.env, http.MethodPut, c.path, body, adminHeaders(), http.StatusOK)
		errorTagsHTTPAssertPutResponse(t, raw, wantSubject, wantQuestion, wantTags...)
		errorTagsHTTPAssertState(t, f.env, c, wantSubject, wantQuestion, wantTags...)
	}

	t.Run("OmittedFieldKeepsTags", func(t *testing.T) {
		c := errorTagsHTTPNewItem(t, f, "omitted")
		assertSuccess(t, c, map[string]any{"question_text": "订正后的题干"},
			"math", "订正后的题干", c.mathA, c.mathB)
	})

	t.Run("NullKeepsTags", func(t *testing.T) {
		c := errorTagsHTTPNewItem(t, f, "null")
		assertSuccess(t, c,
			map[string]any{"knowledge_tag_ids": nil, "question_text": "订正后的题干2"},
			"math", "订正后的题干2", c.mathA, c.mathB)
	})

	t.Run("EmptyArrayClearsTags", func(t *testing.T) {
		c := errorTagsHTTPNewItem(t, f, "empty")
		assertSuccess(t, c, map[string]any{"knowledge_tag_ids": []int64{}},
			"math", c.originalQuestion)
		errorTagsHTTPAssertLinkCounts(t, f.env, c.itemID, 0)
	})

	// 替换而非追加：旧 A 必须消失，重复的 B/C 只各留一条链接。
	t.Run("DeduplicatedReplace", func(t *testing.T) {
		c := errorTagsHTTPNewItem(t, f, "replace")
		assertSuccess(t, c,
			map[string]any{"knowledge_tag_ids": []int64{c.mathC, c.mathB, c.mathC, c.mathB, c.mathC}},
			"math", c.originalQuestion, c.mathB, c.mathC)
		errorTagsHTTPAssertLinkCounts(t, f.env, c.itemID, 2)
	})

	// 同请求改 subject 到 chinese 并绑 chinese 标签：按最终 subject 校验通过。
	t.Run("SubjectChangeWithMatchingTagSucceeds", func(t *testing.T) {
		c := errorTagsHTTPNewItem(t, f, "subject-ok")
		assertSuccess(t, c, map[string]any{
			"subject":           "chinese",
			"question_text":     "语文化后的题干",
			"knowledge_tag_ids": []int64{c.chineseA, c.chineseA},
		}, "chinese", "语文化后的题干", c.chineseA)
		errorTagsHTTPAssertLinkCounts(t, f.env, c.itemID, 1)
	})

	t.Run("DuplicateIDsDoNotCreateDuplicateLinks", func(t *testing.T) {
		c := errorTagsHTTPNewItem(t, f, "dups")
		assertSuccess(t, c,
			map[string]any{"knowledge_tag_ids": []int64{
				c.mathA, c.mathA, c.mathB, c.mathB, c.mathC, c.mathC, c.mathC,
			}},
			"math", c.originalQuestion, c.mathA, c.mathB, c.mathC)
		errorTagsHTTPAssertLinkCounts(t, f.env, c.itemID, 3)
	})
}

// 顶层四：改 subject 但 knowledge_tag_ids 缺省（R2 冻结契约）。旧标签与
// 新学科不兼容必须 400 且零写；兼容（或本来无标签）才允许，并把改字段的
// 整条路径（字段 + subject）都覆盖。最后一个是真实 HTTP 并发：DELETE 与
// PUT 重叠时，重读发现行已消失必须 404，而不是 200 + null。
func TestUpdateErrorItemKnowledgeTagsSubjectChangeGuard(t *testing.T) {
	f := errorTagsHTTPSetup(t)

	// 旧标签是 math，改成 chinese 却缺省 tags：整单回滚。
	t.Run("SubjectChangeWithoutTagsAndCrossSubjectOldTagsIs400", func(t *testing.T) {
		c := errorTagsHTTPNewItem(t, f, "guard-reject")
		raw := errorTagsHTTPRequest(t, f.env, http.MethodPut, c.path, map[string]any{
			"subject":          "chinese",
			"question_text":    "不应落库",
			"mistake_analysis": "不应落库",
			"error_type":       "concept",
		}, adminHeaders(), http.StatusBadRequest)
		errorTagsHTTPAssertErrorJSON(t, raw)
		errorTagsHTTPAssertState(t, f.env, c, "math", c.originalQuestion, c.mathA, c.mathB)
		var errType, mistakeAnalysis string
		if err := f.env.db.QueryRow(`SELECT COALESCE(error_type,''), mistake_analysis FROM error_items WHERE id = ?`, c.itemID).
			Scan(&errType, &mistakeAnalysis); err != nil {
			t.Fatalf("读取 error_items 其它字段: %v", err)
		}
		if errType != "" || mistakeAnalysis != "" {
			t.Fatalf("拒绝的请求不得写入任何字段, error_type=%q mistake_analysis=%q", errType, mistakeAnalysis)
		}
		errorTagsHTTPAssertLinkCounts(t, f.env, c.itemID, 2)
	})

	// 旧标签都属于新学科（先换成 chinese 并绑上 chinese 标签），再次
	// “改 subject + 缺省 tags” 必须放行且标签保留。
	t.Run("SubjectChangeWithoutTagsAndCompatibleOldTagsSucceeds", func(t *testing.T) {
		c := errorTagsHTTPNewItem(t, f, "guard-ok")
		errorTagsHTTPRequest(t, f.env, http.MethodPut, c.path, map[string]any{
			"subject":           "chinese", // 旧 math 标签 → 必须同时清掉
			"knowledge_tag_ids": []int64{c.chineseA},
		}, adminHeaders(), http.StatusOK)
		errorTagsHTTPAssertState(t, f.env, c, "chinese", c.originalQuestion, c.chineseA)
		raw := errorTagsHTTPRequest(t, f.env, http.MethodPut, c.path, map[string]any{
			"subject":       "chinese", // 同值，非切换
			"question_text": "兼容切换后的题干",
		}, adminHeaders(), http.StatusOK)
		errorTagsHTTPAssertPutResponse(t, raw, "chinese", "兼容切换后的题干", c.chineseA)
		errorTagsHTTPAssertState(t, f.env, c, "chinese", "兼容切换后的题干", c.chineseA)
	})

	// 无标签的错题：改 subject 缺省 tags 合法，且不会凭空建标签。
	t.Run("SubjectChangeWithoutTagsOnTaglessItemSucceeds", func(t *testing.T) {
		c := errorTagsHTTPNewItem(t, f, "guard-tagless")
		errorTagsHTTPRequest(t, f.env, http.MethodPut, c.path,
			map[string]any{"knowledge_tag_ids": []int64{}}, adminHeaders(), http.StatusOK)
		errorTagsHTTPAssertState(t, f.env, c, "math", c.originalQuestion)
		errorTagsHTTPRequest(t, f.env, http.MethodPut, c.path,
			map[string]any{"subject": "english"}, adminHeaders(), http.StatusOK)
		errorTagsHTTPAssertState(t, f.env, c, "english", c.originalQuestion)
		errorTagsHTTPAssertLinkCounts(t, f.env, c.itemID, 0)
	})
}

// errorTagsHTTPConcurrentPutDelete fires one PUT and one DELETE at the same
// row from two goroutines. With MaxOpenConns=1 they serialise: either the
// PUT lands first (then the DELETE removes it) or the DELETE lands first
// (then the PUT must see the row vanish on its re-read and answer 404).
// Each ordering has an exact assertion, so a lost concurrent-delete guard
// (200 + null, or a write against a deleted row) fails every run.
func errorTagsHTTPConcurrentPutDelete(t *testing.T, f *errorTagsHTTPFixture, c *errorTagsHTTPCase, putBody map[string]any) {
	t.Helper()
	start := make(chan struct{})
	putCode := make(chan int, 1)
	putRaw := make(chan []byte, 1)
	delCode := make(chan int, 1)
	go func() {
		<-start
		resp := f.env.request(t, http.MethodPut, c.path, putBody, adminHeaders())
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		putCode <- resp.StatusCode
		putRaw <- raw
	}()
	go func() {
		<-start
		resp := f.env.request(t, http.MethodDelete, c.path, nil, adminHeaders())
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		delCode <- resp.StatusCode
	}()
	close(start)
	pc, dc, raw := <-putCode, <-delCode, <-putRaw
	if dc != http.StatusOK {
		t.Fatalf("并发 DELETE 状态 = %d, want 200", dc)
	}

	// 合法顺序只有两种：PUT 的写事务先拿到唯一连接（PUT 200，响应是完整
	// 对象，DELETE 随后删除），或 DELETE 先提交（PUT 要么在入口 GET 就
	// 404，要么更新命中 0 行 / 重读 nil → 404）。任何 200/null、500、幽灵
	// 链接都是缺陷；无论哪种顺序，最终持久状态都只能是「行已删除」。
	switch pc {
	case http.StatusOK:
		item, _ := errorTagsHTTPDecode(t, raw, "并发 PUT 响应")
		if item.ID != c.itemID {
			t.Fatalf("并发 PUT 响应 id = %d, want %d; body = %s", item.ID, c.itemID, raw)
		}
	case http.StatusNotFound:
		errorTagsHTTPAssertErrorJSON(t, raw)
	default:
		t.Fatalf("并发 PUT 状态 = %d, want 200 或 404; body = %s", pc, raw)
	}

	var rows int
	if err := f.env.db.QueryRow(`SELECT COUNT(*) FROM error_items WHERE id = ?`, c.itemID).Scan(&rows); err != nil {
		t.Fatalf("统计 error_items: %v", err)
	}
	if rows != 0 {
		t.Fatalf("并发 DELETE 200 后行仍存在（剩 %d 行），DELETE 未生效", rows)
	}
	errorTagsHTTPAssertLinkCounts(t, f.env, c.itemID, 0)

	// 确定性覆盖「重读 nil → 404」分支（不依赖调度）：DELETE 先落库，再对
	// 同一 id PUT 一个仅改字段、subject 不变的 body。该路径不经过 subject
	// 守卫，也不经过标签事务的 RowsAffected 检查（UpdateErrorItemFields 不
	// 回报影响行数），只能靠入口 GET 后的重读发现行已消失；重读守卫丢失时
	// 这里会得到 200 + null。
	gone := fmt.Sprintf("/api/admin/error-items/%d", c.itemID)
	rawGone := errorTagsHTTPRequest(t, f.env, http.MethodPut, gone,
		map[string]any{"question_text": "删除后更新"}, adminHeaders(), http.StatusNotFound)
	errorTagsHTTPAssertErrorJSON(t, rawGone)
	errorTagsHTTPAssertLinkCounts(t, f.env, c.itemID, 0)
}

func TestUpdateErrorItemConcurrentDelete(t *testing.T) {
	f := errorTagsHTTPSetup(t)

	t.Run("ConcurrentDeleteWithTags", func(t *testing.T) {
		c := errorTagsHTTPNewItem(t, f, "race-tags")
		errorTagsHTTPConcurrentPutDelete(t, f, c, map[string]any{"knowledge_tag_ids": []int64{c.mathA, c.mathB}})
	})
	t.Run("ConcurrentDeleteFieldsOnly", func(t *testing.T) {
		c := errorTagsHTTPNewItem(t, f, "race-fields")
		errorTagsHTTPConcurrentPutDelete(t, f, c, map[string]any{"question_text": "并发字段"})
	})
}
