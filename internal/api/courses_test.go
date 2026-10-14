package api_test

import (
	"net/http"
	"testing"
)

// L2 course system: the whole-pipeline test. A parent builds a course
// (units with prerequisites, resources with the four Canvas completion
// predicates), publishes it, the kid walks the tree (locked units carry
// lock_explanations), reports progress until the predicates pass, the
// next unit unlocks, and the mastery (outcomes) view rolls the scores
// up by calculation_method.

// seedCourse builds a course through the admin API: two units —
// "自然拼读" (unlocked from the start) and "绘本精读" (requires the
// first) — with resources covering must_view / min_minutes / min_score,
// plus one outcome (latest rollup).
func seedCourse(t *testing.T, env *testEnv, kidID int) (courseID, unit1, unit2, resView, resMinutes, resPractice int64) {
	t.Helper()
	resp := env.request(t, "POST", "/api/admin/courses", map[string]any{
		"title":       "英语阅读养成",
		"description": "从拼读到自主阅读",
		"user_id":     kidID,
	}, adminHeaders())
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create course: %d", resp.StatusCode)
	}
	var course map[string]any
	decodeBody(t, resp, &course)
	courseID = int64(course["id"].(float64))

	resp = env.request(t, "POST", "/api/admin/courses/"+itoa(int(courseID))+"/units", map[string]any{
		"title": "自然拼读", "published": true,
	}, adminHeaders())
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create unit1: %d", resp.StatusCode)
	}
	var u1 map[string]any
	decodeBody(t, resp, &u1)
	unit1 = int64(u1["id"].(float64))

	resp = env.request(t, "POST", "/api/admin/units/"+itoa(int(unit1))+"/resources", map[string]any{
		"title": "拼读动画", "resource_type": "video", "content_ref": "/files/phonics.mp4",
		"completion_type": "must_view",
	}, adminHeaders())
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create resource view: %d", resp.StatusCode)
	}
	var r1 map[string]any
	decodeBody(t, resp, &r1)
	resView = int64(r1["id"].(float64))

	resp = env.request(t, "POST", "/api/admin/units/"+itoa(int(unit1))+"/resources", map[string]any{
		"title": "绘本跟读", "resource_type": "audio", "content_ref": "/files/read.mp3",
		"completion_type": "min_minutes", "min_minutes": 30,
	}, adminHeaders())
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create resource minutes: %d", resp.StatusCode)
	}
	var r2 map[string]any
	decodeBody(t, resp, &r2)
	resMinutes = int64(r2["id"].(float64))

	resp = env.request(t, "POST", "/api/admin/courses/"+itoa(int(courseID))+"/units", map[string]any{
		"title": "绘本精读", "published": true,
		"prerequisite_unit_ids": []int64{unit1},
	}, adminHeaders())
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create unit2: %d", resp.StatusCode)
	}
	var u2 map[string]any
	decodeBody(t, resp, &u2)
	unit2 = int64(u2["id"].(float64))

	resp = env.request(t, "POST", "/api/admin/units/"+itoa(int(unit2))+"/resources", map[string]any{
		"title": "阅读理解练习", "resource_type": "practice", "content_ref": "",
		"completion_type": "min_score", "min_score": 80,
	}, adminHeaders())
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create resource practice: %d", resp.StatusCode)
	}
	var r3 map[string]any
	decodeBody(t, resp, &r3)
	resPractice = int64(r3["id"].(float64))

	resp = env.request(t, "POST", "/api/admin/courses/"+itoa(int(courseID))+"/outcomes", map[string]any{
		"title": "能自主读完一本章节书", "mastery_points": 80, "calculation_method": "latest",
	}, adminHeaders())
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create outcome: %d", resp.StatusCode)
	}
	return courseID, unit1, unit2, resView, resMinutes, resPractice
}

func TestCourseLifecycleAndUnlockEngine(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "小明")
	courseID, _, _, resView, resMinutes, resPractice := seedCourse(t, env, kid)

	// Draft courses are invisible to the kid (edX semantics).
	var kidCourses []map[string]any
	decodeBody(t, env.expectStatus(t, "GET", "/api/courses", nil, childHeaders(kid), http.StatusOK), &kidCourses)
	if len(kidCourses) != 0 {
		t.Fatalf("draft course must be invisible, kid sees %d", len(kidCourses))
	}

	// Publish.
	env.expectStatus(t, "POST", "/api/admin/courses/"+itoa(int(courseID))+"/publish", nil, adminHeaders(), http.StatusOK)

	// Now the kid sees one course, 0% progress.
	decodeBody(t, env.expectStatus(t, "GET", "/api/courses", nil, childHeaders(kid), http.StatusOK), &kidCourses)
	if len(kidCourses) != 1 {
		t.Fatalf("expected 1 published course, got %d", len(kidCourses))
	}
	if int(kidCourses[0]["progress_percent"].(float64)) != 0 {
		t.Fatalf("expected 0%% progress, got %v", kidCourses[0]["progress_percent"])
	}

	// The tree: unit1 unlocked, unit2 locked with the prerequisite
	// explanation naming unit1's title (Canvas lock_explanation).
	var tree map[string]any
	decodeBody(t, env.expectStatus(t, "GET", "/api/courses/"+itoa(int(courseID)), nil, childHeaders(kid), http.StatusOK), &tree)
	units := tree["units"].([]any)
	if len(units) != 2 {
		t.Fatalf("expected 2 units, got %d", len(units))
	}
	u1 := units[0].(map[string]any)
	u2 := units[1].(map[string]any)
	if u1["state"] != "unlocked" {
		t.Fatalf("unit1 should be unlocked, got %v", u1["state"])
	}
	if u2["state"] != "locked" {
		t.Fatalf("unit2 should be locked, got %v", u2["state"])
	}
	if expl, _ := u2["lock_explanation"].(string); expl == "" {
		t.Fatalf("locked unit needs a lock_explanation")
	}

	// A locked unit's resources reject progress reports.
	env.expectStatus(t, "POST", "/api/resources/"+itoa(int(resPractice))+"/progress",
		map[string]any{"score": 90}, childHeaders(kid), http.StatusBadRequest)

	// Progress on unit1: view passes at >0 seconds, minutes at 30*60.
	var prog map[string]any
	decodeBody(t, env.expectStatus(t, "POST", "/api/resources/"+itoa(int(resView))+"/progress",
		map[string]any{"viewed_seconds": 5}, childHeaders(kid), http.StatusOK), &prog)
	unitState := prog["unit"].(map[string]any)
	if unitState["state"] != "started" {
		t.Fatalf("unit1 should be started after one of two done, got %v", unitState["state"])
	}

	// min_minutes at 29 minutes is not enough.
	decodeBody(t, env.expectStatus(t, "POST", "/api/resources/"+itoa(int(resMinutes))+"/progress",
		map[string]any{"viewed_seconds": 29 * 60}, childHeaders(kid), http.StatusOK), &prog)
	unitState = prog["unit"].(map[string]any)
	if unitState["state"] != "started" {
		t.Fatalf("29/30 minutes must not complete the unit, got %v", unitState["state"])
	}

	// 30 minutes completes unit1, which unlocks unit2 in the same
	// course on the next tree fetch.
	decodeBody(t, env.expectStatus(t, "POST", "/api/resources/"+itoa(int(resMinutes))+"/progress",
		map[string]any{"viewed_seconds": 30 * 60}, childHeaders(kid), http.StatusOK), &prog)
	unitState = prog["unit"].(map[string]any)
	if unitState["state"] != "completed" {
		t.Fatalf("unit1 should complete at 30 minutes, got %v", unitState["state"])
	}

	decodeBody(t, env.expectStatus(t, "GET", "/api/courses/"+itoa(int(courseID)), nil, childHeaders(kid), http.StatusOK), &tree)
	units = tree["units"].([]any)
	u2 = units[1].(map[string]any)
	if u2["state"] != "unlocked" {
		t.Fatalf("unit2 should unlock after unit1 completes, got %v", u2["state"])
	}
	if int(tree["progress_percent"].(float64)) != 50 {
		t.Fatalf("expected 50%% course progress, got %v", tree["progress_percent"])
	}

	// min_score: 79 fails the predicate, 80 passes.
	decodeBody(t, env.expectStatus(t, "POST", "/api/resources/"+itoa(int(resPractice))+"/progress",
		map[string]any{"score": 79}, childHeaders(kid), http.StatusOK), &prog)
	unitState = prog["unit"].(map[string]any)
	if unitState["state"] != "started" {
		t.Fatalf("79 < 80 must not complete, got %v", unitState["state"])
	}
	decodeBody(t, env.expectStatus(t, "POST", "/api/resources/"+itoa(int(resPractice))+"/progress",
		map[string]any{"score": 80}, childHeaders(kid), http.StatusOK), &prog)
	unitState = prog["unit"].(map[string]any)
	if unitState["state"] != "completed" {
		t.Fatalf("80 >= 80 must complete, got %v", unitState["state"])
	}

	decodeBody(t, env.expectStatus(t, "GET", "/api/courses/"+itoa(int(courseID)), nil, childHeaders(kid), http.StatusOK), &tree)
	if int(tree["progress_percent"].(float64)) != 100 {
		t.Fatalf("expected 100%% course progress, got %v", tree["progress_percent"])
	}

	// The kid cannot see another kid's course.
	other := env.createChild(t, "小红")
	env.expectStatus(t, "GET", "/api/courses/"+itoa(int(courseID)), nil, childHeaders(other), http.StatusNotFound)
}

func TestCourseCloneRewritesPrerequisites(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "小明")
	courseID, _, _, _, _, _ := seedCourse(t, env, kid)
	other := env.createChild(t, "小红")

	// Clone to 小红: the tree copies, prerequisites point at the NEW
	// unit ids, and the clone starts as a draft.
	var clone map[string]any
	decodeBody(t, env.expectStatus(t, "POST", "/api/admin/courses/"+itoa(int(courseID))+"/clone",
		map[string]any{"user_id": other, "title": "英语阅读养成（第二期）"}, adminHeaders(), http.StatusCreated), &clone)
	cloneID := int64(clone["id"].(float64))
	if clone["status"] != "draft" {
		t.Fatalf("clone must start as draft, got %v", clone["status"])
	}

	var full map[string]any
	decodeBody(t, env.expectStatus(t, "GET", "/api/admin/courses/"+itoa(int(cloneID)), nil, adminHeaders(), http.StatusOK), &full)
	units := full["units"].([]any)
	if len(units) != 2 {
		t.Fatalf("clone must carry both units, got %d", len(units))
	}
	u2 := units[1].(map[string]any)
	prereqs := u2["prerequisite_unit_ids"].([]any)
	if len(prereqs) != 1 {
		t.Fatalf("clone must keep one prerequisite, got %v", prereqs)
	}
	newPrereq := int64(prereqs[0].(float64))
	newUnit1 := int64(units[0].(map[string]any)["id"].(float64))
	if newPrereq != newUnit1 {
		t.Fatalf("clone prerequisite must be rewritten to the new unit id: want %d got %d", newUnit1, newPrereq)
	}
	// The cloned unit2 carries one resource (min_score 80).
	resources := u2["resources"].([]any)
	if len(resources) != 1 {
		t.Fatalf("clone must carry unit2's resource, got %d", len(resources))
	}
}

func TestCoursePreviewAndViewAsLearner(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "小明")
	courseID, unit1, _, resView, _, _ := seedCourse(t, env, kid)
	env.expectStatus(t, "POST", "/api/admin/courses/"+itoa(int(courseID))+"/publish", nil, adminHeaders(), http.StatusOK)

	// The kid makes partial progress.
	env.expectStatus(t, "POST", "/api/resources/"+itoa(int(resView))+"/progress",
		map[string]any{"viewed_seconds": 5}, childHeaders(kid), http.StatusOK)

	// The admin's View As Learner preview shows the kid's exact tree:
	// unit1 started, unit2 locked with the explanation.
	var preview map[string]any
	decodeBody(t, env.expectStatus(t, "POST", "/api/admin/courses/preview",
		map[string]any{"course_id": courseID}, adminHeaders(), http.StatusOK), &preview)
	units := preview["units"].([]any)
	u1 := units[0].(map[string]any)
	u2 := units[1].(map[string]any)
	if u1["state"] != "started" {
		t.Fatalf("preview unit1 should mirror the kid's started state, got %v", u1["state"])
	}
	if u2["state"] != "locked" {
		t.Fatalf("preview unit2 should be locked, got %v", u2["state"])
	}
	_ = unit1
}

func TestCourseOutcomesRollup(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "小明")
	courseID, _, _, _, _, _ := seedCourse(t, env, kid)
	env.expectStatus(t, "POST", "/api/admin/courses/"+itoa(int(courseID))+"/publish", nil, adminHeaders(), http.StatusOK)

	// Find the seeded outcome (latest, mastery 80).
	var outcomes []map[string]any
	decodeBody(t, env.expectStatus(t, "GET", "/api/admin/courses/"+itoa(int(courseID))+"/outcomes", nil, adminHeaders(), http.StatusOK), &outcomes)
	if len(outcomes) != 1 {
		t.Fatalf("expected 1 outcome, got %d", len(outcomes))
	}
	outcomeID := int64(outcomes[0]["id"].(float64))

	// No assessments yet: no score, not mastered.
	decodeBody(t, env.expectStatus(t, "GET", "/api/courses/"+itoa(int(courseID))+"/outcomes", nil, childHeaders(kid), http.StatusOK), &outcomes)
	if outcomes[0]["score"] != nil {
		t.Fatalf("no assessments must mean no score, got %v", outcomes[0]["score"])
	}

	// Parent scores 60 then 85: latest rollup keeps 85 (>= 80 mastered).
	env.expectStatus(t, "POST", "/api/admin/outcomes/"+itoa(int(outcomeID))+"/assessments",
		map[string]any{"user_id": kid, "score": 60}, adminHeaders(), http.StatusCreated)
	env.expectStatus(t, "POST", "/api/admin/outcomes/"+itoa(int(outcomeID))+"/assessments",
		map[string]any{"user_id": kid, "score": 85}, adminHeaders(), http.StatusCreated)
	decodeBody(t, env.expectStatus(t, "GET", "/api/courses/"+itoa(int(courseID))+"/outcomes", nil, childHeaders(kid), http.StatusOK), &outcomes)
	if got := int(outcomes[0]["score"].(float64)); got != 85 {
		t.Fatalf("latest rollup must be 85, got %d", got)
	}
	if outcomes[0]["mastered"] != true {
		t.Fatalf("85 >= 80 must be mastered")
	}

	// A decaying_average outcome (65/35): scores 100 then 40 →
	// 40*65 + 100*35 = 61.
	resp := env.request(t, "POST", "/api/admin/courses/"+itoa(int(courseID))+"/outcomes", map[string]any{
		"title": "口语流利度", "mastery_points": 80, "calculation_method": "decaying_average", "calc_int": 65,
	}, adminHeaders())
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create decaying outcome: %d", resp.StatusCode)
	}
	var o2 map[string]any
	decodeBody(t, resp, &o2)
	o2ID := int64(o2["id"].(float64))
	env.expectStatus(t, "POST", "/api/admin/outcomes/"+itoa(int(o2ID))+"/assessments",
		map[string]any{"user_id": kid, "score": 100}, adminHeaders(), http.StatusCreated)
	env.expectStatus(t, "POST", "/api/admin/outcomes/"+itoa(int(o2ID))+"/assessments",
		map[string]any{"user_id": kid, "score": 40}, adminHeaders(), http.StatusCreated)
	decodeBody(t, env.expectStatus(t, "GET", "/api/courses/"+itoa(int(courseID))+"/outcomes", nil, childHeaders(kid), http.StatusOK), &outcomes)
	var rolled *int
	for _, o := range outcomes {
		if int64(o["id"].(float64)) == o2ID && o["score"] != nil {
			v := int(o["score"].(float64))
			rolled = &v
		}
	}
	if rolled == nil || *rolled != 61 {
		t.Fatalf("decaying_average 65/35 of [100,40] must be 61, got %v", rolled)
	}
}

func TestCourseValidationAndGuards(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "小明")

	// Title and user_id are required.
	env.expectStatus(t, "POST", "/api/admin/courses", map[string]any{"title": ""}, adminHeaders(), http.StatusBadRequest)
	env.expectStatus(t, "POST", "/api/admin/courses", map[string]any{"title": "X", "user_id": 0}, adminHeaders(), http.StatusBadRequest)

	resp := env.expectStatus(t, "POST", "/api/admin/courses", map[string]any{
		"title": "课程", "user_id": kid,
	}, adminHeaders(), http.StatusCreated)
	var course map[string]any
	decodeBody(t, resp, &course)
	courseID := int64(course["id"].(float64))

	// Units: cross-course prerequisites are rejected.
	resp = env.expectStatus(t, "POST", "/api/admin/courses/"+itoa(int(courseID))+"/units",
		map[string]any{"title": "单元一", "published": true}, adminHeaders(), http.StatusCreated)
	var u1 map[string]any
	decodeBody(t, resp, &u1)
	unitID := int64(u1["id"].(float64))

	resp = env.expectStatus(t, "POST", "/api/admin/courses", map[string]any{
		"title": "另一课程", "user_id": kid,
	}, adminHeaders(), http.StatusCreated)
	var otherCourse map[string]any
	decodeBody(t, resp, &otherCourse)
	env.expectStatus(t, "POST", "/api/admin/courses/"+itoa(int(otherCourse["id"].(float64)))+"/units",
		map[string]any{"title": "外来单元", "prerequisite_unit_ids": []int64{unitID}}, adminHeaders(), http.StatusBadRequest)

	// A unit cannot require itself.
	env.expectStatus(t, "PUT", "/api/admin/units/"+itoa(int(unitID)),
		map[string]any{"title": "单元一", "prerequisite_unit_ids": []int64{unitID}}, adminHeaders(), http.StatusBadRequest)

	// Resources: invalid type / predicate / threshold combos rejected.
	env.expectStatus(t, "POST", "/api/admin/units/"+itoa(int(unitID))+"/resources",
		map[string]any{"title": "R", "resource_type": "hologram"}, adminHeaders(), http.StatusBadRequest)
	env.expectStatus(t, "POST", "/api/admin/units/"+itoa(int(unitID))+"/resources",
		map[string]any{"title": "R", "resource_type": "practice", "completion_type": "telepathy"}, adminHeaders(), http.StatusBadRequest)
	env.expectStatus(t, "POST", "/api/admin/units/"+itoa(int(unitID))+"/resources",
		map[string]any{"title": "R", "resource_type": "practice", "completion_type": "min_score"}, adminHeaders(), http.StatusBadRequest)
	env.expectStatus(t, "POST", "/api/admin/units/"+itoa(int(unitID))+"/resources",
		map[string]any{"title": "R", "resource_type": "audio", "completion_type": "min_minutes"}, adminHeaders(), http.StatusBadRequest)

	// Display-only resources (no completion_type) are fine and never
	// gate: a unit of only display-only resources stays unlocked.
	resp = env.expectStatus(t, "POST", "/api/admin/units/"+itoa(int(unitID))+"/resources",
		map[string]any{"title": "说明页", "resource_type": "page", "content_ref": "# 欢迎"}, adminHeaders(), http.StatusCreated)
	_ = resp

	// Kids can't touch the admin surface.
	env.expectStatus(t, "GET", "/api/admin/courses", nil, childHeaders(kid), http.StatusForbidden)
	env.expectStatus(t, "POST", "/api/admin/courses", map[string]any{"title": "X", "user_id": kid}, childHeaders(kid), http.StatusForbidden)
}

func TestCourseTimedUnlock(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "小明")

	resp := env.expectStatus(t, "POST", "/api/admin/courses", map[string]any{
		"title": "假期课程", "user_id": kid,
	}, adminHeaders(), http.StatusCreated)
	var course map[string]any
	decodeBody(t, resp, &course)
	courseID := int64(course["id"].(float64))

	// unlock_at 24h out: the unit stays locked with the timed message.
	resp = env.expectStatus(t, "POST", "/api/admin/courses/"+itoa(int(courseID))+"/units", map[string]any{
		"title": "假期单元", "published": true,
		"unlock_at": "2999-01-01T00:00:00Z",
	}, adminHeaders(), http.StatusCreated)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create timed unit: %d", resp.StatusCode)
	}
	env.expectStatus(t, "POST", "/api/admin/courses/"+itoa(int(courseID))+"/publish", nil, adminHeaders(), http.StatusOK)

	var tree map[string]any
	decodeBody(t, env.expectStatus(t, "GET", "/api/courses/"+itoa(int(courseID)), nil, childHeaders(kid), http.StatusOK), &tree)
	units := tree["units"].([]any)
	u := units[0].(map[string]any)
	if u["state"] != "locked" {
		t.Fatalf("timed unit must be locked, got %v", u["state"])
	}
	if expl, _ := u["lock_explanation"].(string); expl == "" {
		t.Fatalf("timed lock needs its explanation")
	}
}
