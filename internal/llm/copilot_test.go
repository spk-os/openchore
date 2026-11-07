package llm

import (
	"strings"
	"testing"
)

// copilotTestSubjects is the ten-subject vocabulary (042) the media
// library and the error book share, minus the "" household bucket.
var copilotTestSubjects = []string{"math", "chinese", "english", "physics", "chemistry", "science", "morality", "history", "geography", "biology"}

// TestCopilotSchemaSubjectEnumCoversTen pins the schema's subject enum:
// a task card drafted in any of the ten subjects (042's widened
// vocabulary) must be a legal structured-output value — the old
// five-subject enum silently dropped the other five.
func TestCopilotSchemaSubjectEnumCoversTen(t *testing.T) {
	props := copilotSchema.Schema["properties"].(map[string]any)
	tasks := props["tasks"].(map[string]any)
	items := tasks["items"].(map[string]any)
	taskProps := items["properties"].(map[string]any)
	enum := taskProps["subject"].(map[string]any)["enum"].([]string)

	want := append([]string{""}, copilotTestSubjects...)
	if len(enum) != len(want) {
		t.Fatalf("subject enum = %v, want %v", enum, want)
	}
	for i, s := range want {
		if enum[i] != s {
			t.Fatalf("subject enum = %v, want %v", enum, want)
		}
	}
}

// TestParseCopilotDraftKeepsTenSubjects: the parser must keep a task
// drafted with any of the ten subjects (the media library's chips filter
// on them) and still fold junk to "" — the fold must not silently drop
// a valid subject.
func TestParseCopilotDraftKeepsTenSubjects(t *testing.T) {
	for _, subject := range copilotTestSubjects {
		raw := `{"tasks":[{"title":"作业","category":"core","subject":"` + subject + `","points_value":5,"days":["daily"]}],"events":[],"memos":[],"notes":[]}`
		draft, err := parseCopilotDraft(raw)
		if err != nil {
			t.Fatalf("subject %s: parse: %v", subject, err)
		}
		if len(draft.Tasks) != 1 {
			t.Fatalf("subject %s: expected 1 task, got %d", subject, len(draft.Tasks))
		}
		if draft.Tasks[0].Subject != subject {
			t.Errorf("subject %s was folded to %q — the ten-subject vocabulary must be preserved", subject, draft.Tasks[0].Subject)
		}
	}

	// Junk still folds to "" (the unclassified bucket).
	draft, err := parseCopilotDraft(`{"tasks":[{"title":"锻炼","category":"core","subject":"gym","points_value":5,"days":["daily"]}],"events":[],"memos":[],"notes":[]}`)
	if err != nil {
		t.Fatalf("junk parse: %v", err)
	}
	if draft.Tasks[0].Subject != "" {
		t.Errorf("junk subject = %q, want folded to \"\"", draft.Tasks[0].Subject)
	}
}

// TestCopilotPromptNamesTenSubjects: the instruction text must name all
// ten subjects so a free-form model actually drafts them (the schema
// enum only constrains structured-output servers).
func TestCopilotPromptNamesTenSubjects(t *testing.T) {
	prompt := CopilotPromptBody(CopilotContext{KidName: "小言"}, "下周复习历史和地理")
	for _, subject := range copilotTestSubjects {
		if !strings.Contains(prompt, `"`+subject+`"`) {
			t.Errorf("copilot prompt missing subject %q", subject)
		}
	}
}

// TestPlannerSchemaSourceTypes pins the source-ref type vocabulary:
// diagnostics and blindspots (042) must be legal ref types — the plan
// cites diagnostic findings too.
func TestPlannerSchemaSourceTypes(t *testing.T) {
	props := plannerSchema.Schema["properties"].(map[string]any)
	items := props["source_refs"].(map[string]any)["items"].(map[string]any)
	refProps := items["properties"].(map[string]any)
	enum := refProps["type"].(map[string]any)["enum"].([]string)

	want := []string{"kc_mastery", "learner_events", "error_dist", "student_config", "reward_ladder", "diagnostics", "blindspots"}
	if len(enum) != len(want) {
		t.Fatalf("source_ref type enum = %v, want %v", enum, want)
	}
	for i, s := range want {
		if enum[i] != s {
			t.Fatalf("source_ref type enum = %v, want %v", enum, want)
		}
	}
}

// TestPlannerPromptNamesSourceTypes: the planner instruction must teach
// all seven source-ref types (a free-form model only knows what the
// prompt says).
func TestPlannerPromptNamesSourceTypes(t *testing.T) {
	prompt := buildPlannerPrompt(PlannerContext{CopilotContext: CopilotContext{KidName: "小言"}})
	for _, refType := range []string{"kc_mastery", "learner_events", "error_dist", "student_config", "reward_ladder", "diagnostics", "blindspots"} {
		if !strings.Contains(prompt, `"`+refType+`"`) {
			t.Errorf("planner prompt missing source type %q", refType)
		}
	}
}

// TestParsePlannerDraftKeepsDiagnosticsRefs: refs typed diagnostics or
// blindspots (042's diagnostic findings) survive the parser's type
// cleanup instead of being retyped learner_events.
func TestParsePlannerDraftKeepsDiagnosticsRefs(t *testing.T) {
	raw := `{"source_refs":[
		{"type":"diagnostics","fact":"诊断发现「二次函数」薄弱","id":7},
		{"type":"blindspots","fact":"盲区报告列出「因式分解」"}
	],"proposal":{"new_chores":[{"title":"二次函数补练","category":"required","subject":"math","points_value":6,"days":["daily"],"refs":[{"type":"blindspots","fact":"盲区：二次函数配方法"}]}],"review_boost":[{"kc":"因式分解","reason":"先修薄弱"}],"notes_to_parent":"从先修补起"}}`
	draft, err := parsePlannerDraft(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(draft.SourceRefs) != 2 {
		t.Fatalf("expected 2 source refs, got %d", len(draft.SourceRefs))
	}
	if draft.SourceRefs[0].Type != "diagnostics" {
		t.Errorf("ref 0 type = %q, want diagnostics (must not be retyped)", draft.SourceRefs[0].Type)
	}
	if draft.SourceRefs[1].Type != "blindspots" {
		t.Errorf("ref 1 type = %q, want blindspots (must not be retyped)", draft.SourceRefs[1].Type)
	}
	if len(draft.Proposal.NewChores) != 1 || len(draft.Proposal.NewChores[0].Refs) != 1 {
		t.Fatalf("expected the chore's own ref kept, got %+v", draft.Proposal.NewChores)
	}
	if draft.Proposal.NewChores[0].Refs[0].Type != "blindspots" {
		t.Errorf("chore ref type = %q, want blindspots", draft.Proposal.NewChores[0].Refs[0].Type)
	}
}
