package llm

import (
	"testing"
)

func TestParseTemplateTune(t *testing.T) {
	tune, err := parseTemplateTune(`{"summary":"已把英语类任务加量","changes":[
		{"index":3,"title":"英语听说练习","points":8,"estimated_minutes":25},
		{"index":4,"points":5},
		{"index":1,"title":"","description":"","points":0,"estimated_minutes":0}
	]}`, 5)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if tune.Summary == "" {
		t.Error("summary should be kept")
	}
	if len(tune.Changes) != 2 {
		t.Fatalf("expected 2 valid changes (drop the no-op), got %d", len(tune.Changes))
	}
	if tune.Changes[0].Index != 3 || tune.Changes[0].Points != 8 || tune.Changes[0].EstimatedMinutes != 25 {
		t.Errorf("change 0 not parsed: %+v", tune.Changes[0])
	}
	if tune.Changes[1].Index != 4 || tune.Changes[1].Points != 5 {
		t.Errorf("change 1 not parsed: %+v", tune.Changes[1])
	}
}

func TestParseTemplateTuneWrapped(t *testing.T) {
	// Servers without structured output may wrap the JSON in prose.
	tune, err := parseTemplateTune("Sure! Here you go:\n```json\n{\"summary\":\"ok\",\"changes\":[{\"index\":0,\"points\":6}]}\n```\nHope that helps.", 3)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(tune.Changes) != 1 || tune.Changes[0].Points != 6 {
		t.Errorf("expected the fenced change parsed, got %+v", tune.Changes)
	}
}

func TestParseTemplateTuneDropsOutOfRange(t *testing.T) {
	tune, err := parseTemplateTune(`{"summary":"x","changes":[{"index":9,"points":5}]}`, 5)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(tune.Changes) != 0 {
		t.Errorf("out-of-range index should be dropped, got %+v", tune.Changes)
	}
}

func TestParseTemplateTuneClamps(t *testing.T) {
	tune, err := parseTemplateTune(`{"summary":"x","changes":[{"index":0,"points":99,"estimated_minutes":-5,"title":"保持"}]}`, 1)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(tune.Changes) != 1 {
		t.Fatalf("expected the change kept (title non-empty), got %d", len(tune.Changes))
	}
	if tune.Changes[0].Points != 0 {
		t.Errorf("points above 20 should clamp to 0 (no override), got %d", tune.Changes[0].Points)
	}
	if tune.Changes[0].EstimatedMinutes != 0 {
		t.Errorf("negative minutes should clamp to 0, got %d", tune.Changes[0].EstimatedMinutes)
	}
}

func TestParseTemplateTuneGarbage(t *testing.T) {
	if _, err := parseTemplateTune("not json at all", 1); err == nil {
		t.Fatal("expected an error for non-JSON input")
	}
}
