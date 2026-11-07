package api_test

// TX-COURSE-OUTCOME-V2: practice 进度上报的 outcome 回写。真 SQLite +
// httptest（setupTest 跑全部迁移，无 mock）。核心契约：
//
//   - resource_type=practice 且带 score：0..100 校验（越界 400），
//     content_ref 映到本机 knowledge tag 后，只匹配当前 course 的
//     outcome，成功保存 attempt 后追加一条 OutcomeAssessment
//     （assessment_type=self、artifact_type=resource、artifact_id=resource id）。
//   - 非 practice、无 score、未知 content_ref：不写 assessment。
//   - 重复上报：允许追加，既有 rollup 不变。

import (
	"net/http"
	"testing"

	"github.com/liftedkilt/openchore/internal/model"
)

// seedOutcomeCourse builds one published course with a single unit and:
//   - a practice resource whose content_ref points at the given
//     knowledge-tag external_ref,
//   - an outcome (latest, mastery 80) linked to that same tag through
//     knowledge_tag_id.
//
// The unit has no prerequisites and the course is published, so progress
// reports go through the unlock engine unhindered.
func seedOutcomeCourse(t *testing.T, env *testEnv, kidID int, contentRef string) (courseID, unitID, resID, outcomeID int64) {
	t.Helper()
	resp := env.request(t, "POST", "/api/admin/courses", map[string]any{
		"title":   "分数回写验证课",
		"user_id": kidID,
	}, adminHeaders())
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create course: %d", resp.StatusCode)
	}
	var course map[string]any
	decodeBody(t, resp, &course)
	courseID = int64(course["id"].(float64))

	resp = env.request(t, "POST", "/api/admin/courses/"+itoa(int(courseID))+"/units", map[string]any{
		"title": "练习单元", "published": true,
	}, adminHeaders())
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create unit: %d", resp.StatusCode)
	}
	var u map[string]any
	decodeBody(t, resp, &u)
	unitID = int64(u["id"].(float64))

	resp = env.request(t, "POST", "/api/admin/units/"+itoa(int(unitID))+"/resources", map[string]any{
		"title": "练习", "resource_type": "practice", "content_ref": contentRef,
	}, adminHeaders())
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create practice resource: %d", resp.StatusCode)
	}
	var res map[string]any
	decodeBody(t, resp, &res)
	resID = int64(res["id"].(float64))

	resp = env.request(t, "POST", "/api/admin/courses/"+itoa(int(courseID))+"/outcomes", map[string]any{
		"title": "掌握度", "mastery_points": 80, "calculation_method": "latest",
		"knowledge_tag_id": seedKnowledgeTagRef(t, env, contentRef),
	}, adminHeaders())
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create outcome: %d", resp.StatusCode)
	}
	var o map[string]any
	decodeBody(t, resp, &o)
	outcomeID = int64(o["id"].(float64))

	env.expectStatus(t, "POST", "/api/admin/courses/"+itoa(int(courseID))+"/publish", nil, adminHeaders(), http.StatusOK)
	return courseID, unitID, resID, outcomeID
}

// seedKnowledgeTagRef inserts one knowledge tag with the given
// external_ref (the shape the 042 import writes) and returns its DB id.
func seedKnowledgeTagRef(t *testing.T, env *testEnv, externalRef string) int64 {
	t.Helper()
	res, err := env.db.Exec(`INSERT INTO knowledge_tags (name, subject, external_ref) VALUES (?, ?, ?)`,
		"分数回写知识点", "math", externalRef)
	if err != nil {
		t.Fatalf("seed knowledge tag: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("seed knowledge tag id: %v", err)
	}
	return id
}

// courseOutcomeAssessments counts the kid's assessments for one outcome
// straight from SQLite.
func courseOutcomeAssessments(t *testing.T, env *testEnv, outcomeID, userID int64) (count int, latest *model.OutcomeAssessment) {
	t.Helper()
	rows, err := env.db.Query(`SELECT id, outcome_id, user_id, score, assessment_type,
		COALESCE(artifact_type, ''), artifact_id, COALESCE(comments, ''), created_at
		FROM outcome_assessments WHERE outcome_id = ? AND user_id = ? ORDER BY id`, outcomeID, userID)
	if err != nil {
		t.Fatalf("query assessments: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		count++
		var a model.OutcomeAssessment
		var artifactID any
		if err := rows.Scan(&a.ID, &a.OutcomeID, &a.UserID, &a.Score, &a.AssessmentType,
			&a.ArtifactType, &artifactID, &a.Comments, &a.CreatedAt); err != nil {
			t.Fatalf("scan assessment: %v", err)
		}
		if v, ok := artifactID.(int64); ok {
			a.ArtifactID = &v
		}
		latest = &a
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate assessments: %v", err)
	}
	return count, latest
}

// totalOutcomeAssessments counts the whole outcome_assessments table —
// cross-outcome negative assertions need the global count, not the
// per-outcome one.
func totalOutcomeAssessments(t *testing.T, env *testEnv) int {
	t.Helper()
	var n int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM outcome_assessments`).Scan(&n); err != nil {
		t.Fatalf("count outcome_assessments: %v", err)
	}
	return n
}

// outcomeAssessmentsForUser counts every assessment row of one kid
// across all outcomes and courses.
func outcomeAssessmentsForUser(t *testing.T, env *testEnv, userID int64) int {
	t.Helper()
	var n int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM outcome_assessments WHERE user_id = ?`, userID).Scan(&n); err != nil {
		t.Fatalf("count user assessments: %v", err)
	}
	return n
}

// TestCourseTaxonomyProgressPracticeWritesOutcome covers the happy path:
// a practice report with a score appends one self assessment whose
// artifact is the resource itself, and the outcome's latest rollup
// reflects it.
func TestCourseTaxonomyProgressPracticeWritesOutcome(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "小澜")
	courseID, _, resID, outcomeID := seedOutcomeCourse(t, env, kid, "cnm_progress_p001")

	var prog map[string]any
	decodeBody(t, env.expectStatus(t, "POST", "/api/resources/"+itoa(int(resID))+"/progress",
		map[string]any{"score": 90}, childHeaders(kid), http.StatusOK), &prog)
	if prog["attempt"] == nil {
		t.Fatalf("response missing attempt: %v", prog)
	}

	count, a := courseOutcomeAssessments(t, env, outcomeID, int64(kid))
	if count != 1 {
		t.Fatalf("assessments = %d, want 1", count)
	}
	if a.AssessmentType != model.AssessmentTypeSelf {
		t.Errorf("assessment_type = %q, want %q", a.AssessmentType, model.AssessmentTypeSelf)
	}
	if a.ArtifactType != "resource" {
		t.Errorf("artifact_type = %q, want resource", a.ArtifactType)
	}
	if a.ArtifactID == nil || *a.ArtifactID != resID {
		t.Errorf("artifact_id = %v, want %d", a.ArtifactID, resID)
	}
	if a.Score != 90 {
		t.Errorf("score = %d, want 90", a.Score)
	}

	// The kid-facing outcome view rolls it up (latest, mastery 80).
	var outcomes []map[string]any
	decodeBody(t, env.expectStatus(t, "GET", "/api/courses/"+itoa(int(courseID))+"/outcomes",
		nil, childHeaders(kid), http.StatusOK), &outcomes)
	if len(outcomes) != 1 {
		t.Fatalf("outcomes = %d, want 1", len(outcomes))
	}
	if outcomes[0]["score"] == nil || int(outcomes[0]["score"].(float64)) != 90 {
		t.Errorf("rolled-up score = %v, want 90", outcomes[0]["score"])
	}
	if outcomes[0]["mastered"] != true {
		t.Errorf("90 >= 80 must be mastered")
	}
	if int(outcomes[0]["assessments"].(float64)) != 1 {
		t.Errorf("assessments count = %v, want 1", outcomes[0]["assessments"])
	}
}

// TestCourseTaxonomyProgressRepeatAppends covers repeat reports: each one
// appends another score event, the rollup (latest) follows the newest,
// and nothing about the older rows changes.
func TestCourseTaxonomyProgressRepeatAppends(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "小澜")
	courseID, _, resID, outcomeID := seedOutcomeCourse(t, env, kid, "cnm_progress_p002")

	env.expectStatus(t, "POST", "/api/resources/"+itoa(int(resID))+"/progress",
		map[string]any{"score": 60}, childHeaders(kid), http.StatusOK)
	env.expectStatus(t, "POST", "/api/resources/"+itoa(int(resID))+"/progress",
		map[string]any{"score": 85}, childHeaders(kid), http.StatusOK)

	count, a := courseOutcomeAssessments(t, env, outcomeID, int64(kid))
	if count != 2 {
		t.Fatalf("assessments = %d, want 2 (append, never dedupe)", count)
	}
	if a.Score != 85 {
		t.Errorf("latest assessment score = %d, want 85", a.Score)
	}

	var outcomes []map[string]any
	decodeBody(t, env.expectStatus(t, "GET", "/api/courses/"+itoa(int(courseID))+"/outcomes",
		nil, childHeaders(kid), http.StatusOK), &outcomes)
	if outcomes[0]["score"] == nil || int(outcomes[0]["score"].(float64)) != 85 {
		t.Errorf("latest rollup = %v, want 85", outcomes[0]["score"])
	}
	if int(outcomes[0]["assessments"].(float64)) != 2 {
		t.Errorf("assessments count = %v, want 2", outcomes[0]["assessments"])
	}
}

// TestCourseTaxonomyProgressNonPracticeSkipped covers the negative
// branches: a non-practice resource and a scoreless practice report
// never touch outcome_assessments.
func TestCourseTaxonomyProgressNonPracticeSkipped(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "小澜")
	courseID, _, resID, outcomeID := seedOutcomeCourse(t, env, kid, "cnm_progress_p003")

	// Scoreless practice report (viewed seconds only): no assessment.
	env.expectStatus(t, "POST", "/api/resources/"+itoa(int(resID))+"/progress",
		map[string]any{"viewed_seconds": 120}, childHeaders(kid), http.StatusOK)
	if count, _ := courseOutcomeAssessments(t, env, outcomeID, int64(kid)); count != 0 {
		t.Fatalf("scoreless practice wrote %d assessments, want 0", count)
	}

	// A page resource with a score: resource_type gates it off.
	resp := env.request(t, "POST", "/api/admin/courses/"+itoa(int(courseID))+"/units", map[string]any{
		"title": "阅读单元", "published": true,
	}, adminHeaders())
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create reading unit: %d", resp.StatusCode)
	}
	var u map[string]any
	decodeBody(t, resp, &u)
	resp = env.request(t, "POST", "/api/admin/units/"+itoa(int(u["id"].(float64)))+"/resources", map[string]any{
		"title": "说明页", "resource_type": "page", "content_ref": "cnm_progress_p003",
	}, adminHeaders())
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create page resource: %d", resp.StatusCode)
	}
	var pageRes map[string]any
	decodeBody(t, resp, &pageRes)
	env.expectStatus(t, "POST", "/api/resources/"+itoa(int(pageRes["id"].(float64)))+"/progress",
		map[string]any{"score": 90}, childHeaders(kid), http.StatusOK)
	if count, _ := courseOutcomeAssessments(t, env, outcomeID, int64(kid)); count != 0 {
		t.Fatalf("non-practice resource wrote %d assessments, want 0", count)
	}
}

// TestCourseTaxonomyProgressUnknownRefSkipped covers the unknown
// content_ref branch: no matching knowledge tag means no assessment,
// while the attempt itself still saves normally.
func TestCourseTaxonomyProgressUnknownRefSkipped(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "小澜")
	_, _, resID, outcomeID := seedOutcomeCourse(t, env, kid, "cnm_progress_p004")

	// Point the resource at a ref no tag carries.
	env.mustExec(t, `UPDATE unit_resources SET content_ref = 'cnm_progress_missing' WHERE id = `+itoa(int(resID)))

	var prog map[string]any
	decodeBody(t, env.expectStatus(t, "POST", "/api/resources/"+itoa(int(resID))+"/progress",
		map[string]any{"score": 90}, childHeaders(kid), http.StatusOK), &prog)
	if prog["attempt"] == nil {
		t.Fatalf("attempt must still save for an unknown ref: %v", prog)
	}
	if count, _ := courseOutcomeAssessments(t, env, outcomeID, int64(kid)); count != 0 {
		t.Fatalf("unknown content_ref wrote %d assessments, want 0", count)
	}
}

// TestCourseTaxonomyProgressScoreBounds covers the 0..100 guard: -1 and
// 101 are rejected with 400 and nothing reaches resource_attempts or
// outcome_assessments, while 0 and 100 are valid scores.
func TestCourseTaxonomyProgressScoreBounds(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "小澜")
	courseID, _, resID, outcomeID := seedOutcomeCourse(t, env, kid, "cnm_progress_p005")

	env.expectStatus(t, "POST", "/api/resources/"+itoa(int(resID))+"/progress",
		map[string]any{"score": -1}, childHeaders(kid), http.StatusBadRequest)
	env.expectStatus(t, "POST", "/api/resources/"+itoa(int(resID))+"/progress",
		map[string]any{"score": 101}, childHeaders(kid), http.StatusBadRequest)

	var attempts int
	env.db.QueryRow(`SELECT COUNT(*) FROM resource_attempts WHERE resource_id = ? AND user_id = ?`,
		resID, kid).Scan(&attempts)
	if attempts != 0 {
		t.Errorf("rejected scores must not save attempts, got %d", attempts)
	}
	if count, _ := courseOutcomeAssessments(t, env, outcomeID, int64(kid)); count != 0 {
		t.Errorf("rejected scores must not write assessments, got %d", count)
	}

	// 0 and 100 are inside the scale: both go through and append.
	env.expectStatus(t, "POST", "/api/resources/"+itoa(int(resID))+"/progress",
		map[string]any{"score": 0}, childHeaders(kid), http.StatusOK)
	env.expectStatus(t, "POST", "/api/resources/"+itoa(int(resID))+"/progress",
		map[string]any{"score": 100}, childHeaders(kid), http.StatusOK)
	if count, a := courseOutcomeAssessments(t, env, outcomeID, int64(kid)); count != 2 || a.Score != 100 {
		t.Errorf("0/100 must both append: count=%d latest=%d", count, a.Score)
	}

	// The bounds guard applies to non-practice resources too (the check
	// is on the request, not the resource type).
	resp := env.request(t, "POST", "/api/admin/courses/"+itoa(int(courseID))+"/units", map[string]any{
		"title": "阅读单元", "published": true,
	}, adminHeaders())
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create reading unit: %d", resp.StatusCode)
	}
	var u map[string]any
	decodeBody(t, resp, &u)
	resp = env.request(t, "POST", "/api/admin/units/"+itoa(int(u["id"].(float64)))+"/resources", map[string]any{
		"title": "说明页", "resource_type": "page",
	}, adminHeaders())
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create page resource: %d", resp.StatusCode)
	}
	var pageRes map[string]any
	decodeBody(t, resp, &pageRes)
	env.expectStatus(t, "POST", "/api/resources/"+itoa(int(pageRes["id"].(float64)))+"/progress",
		map[string]any{"score": 101}, childHeaders(kid), http.StatusBadRequest)
}

// TestCourseTaxonomyProgressAssessmentDBError500 covers DEF-COURSE-1: a
// real DB failure while appending the assessment (a SQLite trigger
// RAISE(ABORT) on the outcome_assessments INSERT) must surface as a 500
// — never a fake 200 with the score silently dropped. Dropping the
// trigger and retrying succeeds and the assessment is visible.
func TestCourseTaxonomyProgressAssessmentDBError500(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "小澜")
	_, _, resID, outcomeID := seedOutcomeCourse(t, env, kid, "cnm_progress_p006")

	// A trigger that aborts every assessment insert (the DB-level failure
	// injection: no store mock, real SQLite).
	env.mustExec(t, `CREATE TRIGGER fail_outcome_assessments BEFORE INSERT ON outcome_assessments
		BEGIN SELECT RAISE(ABORT, 'injected failure'); END`)

	env.expectStatus(t, "POST", "/api/resources/"+itoa(int(resID))+"/progress",
		map[string]any{"score": 90}, childHeaders(kid), http.StatusInternalServerError)

	// The attempt row survived (it is written before the assessment
	// stage); the assessment did not.
	var attempts int
	env.db.QueryRow(`SELECT COUNT(*) FROM resource_attempts WHERE resource_id = ? AND user_id = ?`,
		resID, kid).Scan(&attempts)
	if attempts != 1 {
		t.Errorf("attempt rows = %d, want 1 (attempt save precedes the failure)", attempts)
	}
	if count, _ := courseOutcomeAssessments(t, env, outcomeID, int64(kid)); count != 0 {
		t.Errorf("failed insert must leave 0 assessments, got %d", count)
	}

	// Client retry after the failure: drop the trigger, report again —
	// the append path works and the assessment is visible.
	env.mustExec(t, `DROP TRIGGER fail_outcome_assessments`)
	env.expectStatus(t, "POST", "/api/resources/"+itoa(int(resID))+"/progress",
		map[string]any{"score": 90}, childHeaders(kid), http.StatusOK)
	count, a := courseOutcomeAssessments(t, env, outcomeID, int64(kid))
	if count != 1 {
		t.Fatalf("retry after trigger drop: assessments = %d, want 1", count)
	}
	if a.Score != 90 {
		t.Errorf("retried assessment score = %d, want 90", a.Score)
	}
}

// TestCourseTaxonomyProgressSharedRefOnlyCurrentCourse covers
// DEF-COURSE-2: two courses whose outcomes share one external_ref — the
// report only writes the assessment of the course the resource belongs
// to, never the sibling's.
func TestCourseTaxonomyProgressSharedRefOnlyCurrentCourse(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "小澜")

	// Two courses, one shared tag ref. seedOutcomeCourse creates a fresh
	// tag per call, so the sibling course is seeded against the same
	// external_ref by hand below.
	_, _, _, outcomeA := seedOutcomeCourse(t, env, kid, "cnm_progress_shared")

	// Course B: same kid, own unit and practice resource pointing at the
	// SAME external_ref. The tag itself is shared (the one seedOutcomeCourse
	// created above), so course B's outcome binds to that same tag id.
	sharedTagID := tagIDByRef(t, env, "cnm_progress_shared")

	resp := env.request(t, "POST", "/api/admin/courses", map[string]any{
		"title": "同 ref 兄弟课", "user_id": kid,
	}, adminHeaders())
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create course B: %d", resp.StatusCode)
	}
	var courseB map[string]any
	decodeBody(t, resp, &courseB)
	courseBID := int64(courseB["id"].(float64))
	resp = env.request(t, "POST", "/api/admin/courses/"+itoa(int(courseBID))+"/units", map[string]any{
		"title": "练习单元", "published": true,
	}, adminHeaders())
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create course B unit: %d", resp.StatusCode)
	}
	var uB map[string]any
	decodeBody(t, resp, &uB)
	resp = env.request(t, "POST", "/api/admin/units/"+itoa(int(uB["id"].(float64)))+"/resources", map[string]any{
		"title": "练习", "resource_type": "practice", "content_ref": "cnm_progress_shared",
	}, adminHeaders())
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create course B resource: %d", resp.StatusCode)
	}
	var resB map[string]any
	decodeBody(t, resp, &resB)
	resBID := int64(resB["id"].(float64))
	resp = env.request(t, "POST", "/api/admin/courses/"+itoa(int(courseBID))+"/outcomes", map[string]any{
		"title": "兄弟课掌握度", "mastery_points": 80, "calculation_method": "latest",
		"knowledge_tag_id": sharedTagID,
	}, adminHeaders())
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create course B outcome: %d", resp.StatusCode)
	}
	var oB map[string]any
	decodeBody(t, resp, &oB)
	outcomeB := int64(oB["id"].(float64))
	env.expectStatus(t, "POST", "/api/admin/courses/"+itoa(int(courseBID))+"/publish", nil, adminHeaders(), http.StatusOK)

	// Report on course B's resource: only course B's outcome gains the
	// assessment; course A's is untouched.
	env.expectStatus(t, "POST", "/api/resources/"+itoa(int(resBID))+"/progress",
		map[string]any{"score": 70}, childHeaders(kid), http.StatusOK)
	if count, a := courseOutcomeAssessments(t, env, outcomeB, int64(kid)); count != 1 || a.Score != 70 {
		t.Fatalf("course B outcome: assessments=%d latest=%d, want 1/70", count, a.Score)
	}
	if count, _ := courseOutcomeAssessments(t, env, outcomeA, int64(kid)); count != 0 {
		t.Fatalf("course A outcome must stay at 0 assessments, got %d", count)
	}
}

// tagIDByRef resolves one external_ref to its knowledge_tags row id
// (the sibling-course seeding reads back the tag an earlier seed call
// created instead of inserting a duplicate row, which the external_ref
// unique index would reject).
func tagIDByRef(t *testing.T, env *testEnv, externalRef string) int64 {
	t.Helper()
	var id int64
	if err := env.db.QueryRow(`SELECT id FROM knowledge_tags WHERE external_ref = ?`, externalRef).Scan(&id); err != nil {
		t.Fatalf("resolve tag %q: %v", externalRef, err)
	}
	return id
}

// TestCourseTaxonomyProgressForeignKid404 covers DEF-COURSE-3: another
// kid reporting on a resource that is not theirs gets the existing 404
// and not a single assessment row lands anywhere.
func TestCourseTaxonomyProgressForeignKid404(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "小澜")
	foreigner := env.createChild(t, "小北")
	_, _, resID, outcomeID := seedOutcomeCourse(t, env, kid, "cnm_progress_p007")

	env.expectStatus(t, "POST", "/api/resources/"+itoa(int(resID))+"/progress",
		map[string]any{"score": 90}, childHeaders(foreigner), http.StatusNotFound)

	if got := outcomeAssessmentsForUser(t, env, int64(foreigner)); got != 0 {
		t.Fatalf("foreign kid must have 0 assessments, got %d", got)
	}
	if got := totalOutcomeAssessments(t, env); got != 0 {
		t.Fatalf("outcome_assessments must be empty after a 404 report, got %d rows", got)
	}
	_ = outcomeID
}

// TestCourseTaxonomyProgressNegativeVariants adds the remaining skip
// branches: an empty content_ref, an outcome bound to a different tag,
// and a course with no outcomes at all — every one still banks the
// attempt (200) and writes no assessment.
func TestCourseTaxonomyProgressNegativeVariants(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "小澜")

	// Empty content_ref: practice + score, but no ref to resolve.
	courseID, _, resID, outcomeID := seedOutcomeCourse(t, env, kid, "cnm_progress_p008")
	env.mustExec(t, `UPDATE unit_resources SET content_ref = '' WHERE id = `+itoa(int(resID)))
	var prog map[string]any
	decodeBody(t, env.expectStatus(t, "POST", "/api/resources/"+itoa(int(resID))+"/progress",
		map[string]any{"score": 90}, childHeaders(kid), http.StatusOK), &prog)
	if prog["attempt"] == nil {
		t.Fatalf("empty content_ref must still save the attempt: %v", prog)
	}
	if got := totalOutcomeAssessments(t, env); got != 0 {
		t.Fatalf("empty content_ref wrote %d assessments, want 0", got)
	}
	_ = outcomeID

	// The tag exists but the course's outcome is bound to another tag:
	// no match within THIS course, no assessment.
	otherTag := seedKnowledgeTagRef(t, env, "cnm_progress_other_tag")
	resp := env.request(t, "POST", "/api/admin/courses/"+itoa(int(courseID))+"/outcomes", map[string]any{
		"title": "绑定别的知识点", "mastery_points": 80, "calculation_method": "latest",
		"knowledge_tag_id": otherTag,
	}, adminHeaders())
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create other-tag outcome: %d", resp.StatusCode)
	}
	env.mustExec(t, `DELETE FROM course_outcomes WHERE id = `+itoa(int(outcomeID)))
	env.mustExec(t, `UPDATE unit_resources SET content_ref = 'cnm_progress_p008' WHERE id = `+itoa(int(resID)))
	decodeBody(t, env.expectStatus(t, "POST", "/api/resources/"+itoa(int(resID))+"/progress",
		map[string]any{"score": 95}, childHeaders(kid), http.StatusOK), &prog)
	if prog["attempt"] == nil {
		t.Fatalf("other-tag outcome must still save the attempt: %v", prog)
	}
	if got := totalOutcomeAssessments(t, env); got != 0 {
		t.Fatalf("outcome bound to another tag wrote %d assessments, want 0", got)
	}

	// A course with no outcomes at all: attempt banks, nothing to roll.
	noOutcomeCourse, _, noOutcomeRes, _ := seedOutcomeCourse(t, env, kid, "cnm_progress_p009")
	env.mustExec(t, `DELETE FROM course_outcomes WHERE course_id = `+itoa(int(noOutcomeCourse)))
	decodeBody(t, env.expectStatus(t, "POST", "/api/resources/"+itoa(int(noOutcomeRes))+"/progress",
		map[string]any{"score": 88}, childHeaders(kid), http.StatusOK), &prog)
	if prog["attempt"] == nil {
		t.Fatalf("no-outcome course must still save the attempt: %v", prog)
	}
	if got := totalOutcomeAssessments(t, env); got != 0 {
		t.Fatalf("no-outcome course wrote %d assessments, want 0", got)
	}
}
