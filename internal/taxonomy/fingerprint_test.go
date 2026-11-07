package taxonomy

import (
	"testing"
)

// TestPackFingerprintStableAndReal locks the fingerprint contract:
// deterministic across calls, non-empty hex, and derived from the real
// embedded pack (so it can never be a constant that survives a data
// change).
func TestPackFingerprintStableAndReal(t *testing.T) {
	fp1, err := PackFingerprint()
	if err != nil {
		t.Fatalf("PackFingerprint: %v", err)
	}
	fp2, err := PackFingerprint()
	if err != nil {
		t.Fatalf("PackFingerprint (second call): %v", err)
	}
	if fp1 == "" {
		t.Fatal("fingerprint is empty")
	}
	if len(fp1) != 64 {
		t.Fatalf("fingerprint length = %d, want 64 hex chars (sha256), got %q", len(fp1), fp1)
	}
	if fp1 != fp2 {
		t.Fatalf("fingerprint not deterministic: %s vs %s", fp1, fp2)
	}
	for _, c := range fp1 {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			t.Fatalf("fingerprint %q is not lowercase hex", fp1)
		}
	}
}

// TestFingerprintFromSourcesDeterminism covers determinism and
// sensitivity of the pure computation: same inputs → same digest;
// any changed path, byte, or member set → different digest.
func TestFingerprintFromSourcesDeterminism(t *testing.T) {
	base := []fingerprintSource{
		{Path: "data/topics.json", Data: []byte(`{"a":1}`)},
		{Path: "data/mappings/x.json", Data: []byte(`{"m":1}`)},
	}
	same := []fingerprintSource{
		{Path: "data/topics.json", Data: []byte(`{"a":1}`)},
		{Path: "data/mappings/x.json", Data: []byte(`{"m":1}`)},
	}
	if got, want := fingerprintFromSources(base), fingerprintFromSources(same); got != want {
		t.Fatalf("equal inputs produced different digests: %s vs %s", got, want)
	}

	cases := map[string][]fingerprintSource{
		"content byte changed": {
			{Path: "data/topics.json", Data: []byte(`{"a":2}`)},
			{Path: "data/mappings/x.json", Data: []byte(`{"m":1}`)},
		},
		"path changed (same content)": {
			{Path: "data/topics2.json", Data: []byte(`{"a":1}`)},
			{Path: "data/mappings/x.json", Data: []byte(`{"m":1}`)},
		},
		"member dropped": {
			{Path: "data/topics.json", Data: []byte(`{"a":1}`)},
		},
		"member appended": {
			{Path: "data/topics.json", Data: []byte(`{"a":1}`)},
			{Path: "data/mappings/x.json", Data: []byte(`{"m":1}`)},
			{Path: "data/mappings/y.json", Data: []byte(`{"m":2}`)},
		},
	}
	want := fingerprintFromSources(base)
	for name, files := range cases {
		if got := fingerprintFromSources(files); got == want {
			t.Errorf("%s: digest unchanged (%s) — fingerprint is not sensitive to it", name, got)
		}
	}
}

// TestFingerprintContentConcatBoundary locks the guarantee the frozen
// scheme (path bytes then content bytes, per file) actually makes:
// splitting the same content differently across files never collides.
// A naive hash(concat(all contents)) would collide here; the path
// separators make the streams differ.
func TestFingerprintContentConcatBoundary(t *testing.T) {
	split := []fingerprintSource{
		{Path: "data/a.json", Data: []byte(`{"x":1}`)},
		{Path: "data/b.json", Data: []byte(`{"y":2}`)},
	}
	merged := []fingerprintSource{
		{Path: "data/a.json", Data: []byte(`{"x":1}{"y":2}`)},
	}
	if got := fingerprintFromSources(merged); got == fingerprintFromSources(split) {
		t.Fatalf("content-concat collision: %s", got)
	}
	// Same total content, three-way split: still distinct from both.
	threeWay := []fingerprintSource{
		{Path: "data/a.json", Data: []byte(`{"x":`)},
		{Path: "data/b.json", Data: []byte(`1}`)},
		{Path: "data/c.json", Data: []byte(`{"y":2}`)},
	}
	if got := fingerprintFromSources(threeWay); got == fingerprintFromSources(split) {
		t.Fatalf("three-way split collided with two-way: %s", got)
	}
}
