package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	msqlite "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/liftedkilt/openchore/internal/model"
	"github.com/liftedkilt/openchore/internal/store"
	"github.com/liftedkilt/openchore/internal/webhook"
	"github.com/liftedkilt/openchore/migrations"
)

// testSessionSecret signs sessions in the white-box tests, mirroring
// api_test.go's.
var testSessionSecret = []byte("openchore-test-session-secret-0123456789")

// TestGetMySummaryOwnOnly: 问题① — the kid's own summary route answers the
// owner with the stored week summary, another kid with 403, and an
// unwritten month with "" (the client shows "not yet", not an error).
// White-box here (package api): the HTTP surface is exercised end to end
// against NewRouter, only the test env is minted directly instead of
// through the api_test helpers.
func TestGetMySummaryOwnOnly(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=on&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

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
	s := store.New(db)
	d := webhook.NewDispatcher(s)
	sessions := NewSessionManager(testSessionSecret)
	oidcSvc := NewOIDCService(s, sessions, d, nil)
	router, _, _, _ := NewRouter(s, d, Auth{Sessions: sessions, OIDC: oidcSvc})
	server := httptest.NewServer(router)
	defer server.Close()

	if _, err := db.Exec(`INSERT INTO users (name, role) VALUES ('Admin', 'admin'), ('Mika', 'child'), ('Ben', 'child')`); err != nil {
		t.Fatalf("seeding users: %v", err)
	}
	kidID, otherID := 2, 3

	weekStart, _ := periodRange("week", time.Now())
	if err := s.SaveWeeklySummary(context.Background(), int64(kidID),
		weekStart.Format(model.DateFormat), "Mika's week went well"); err != nil {
		t.Fatalf("seeding weekly summary: %v", err)
	}

	get := func(path string, userID int) *http.Response {
		t.Helper()
		req, err := http.NewRequest("GET", server.URL+path, nil)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+SignSessionToken(testSessionSecret, SessionClaims{
			UserID:  int64(userID),
			Method:  SessionMethodPin,
			Expires: time.Now().Add(time.Hour).Unix(),
		}))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		return resp
	}
	check := func(path string, userID, want int) *http.Response {
		t.Helper()
		resp := get(path, userID)
		if resp.StatusCode != want {
			t.Fatalf("GET %s as %d: expected %d, got %d", path, userID, want, resp.StatusCode)
		}
		return resp
	}

	// The owner reads the stored text.
	resp := check("/api/users/"+strconv.Itoa(kidID)+"/my-summary?period=week", kidID, http.StatusOK)
	var out map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["summary"] != "Mika's week went well" {
		t.Fatalf("owner: got summary %q", out["summary"])
	}

	// Another kid gets 403 — the route is strictly one's own.
	check("/api/users/"+strconv.Itoa(kidID)+"/my-summary?period=week", otherID, http.StatusForbidden)

	// A period nothing was written for answers "" (in-progress or never run).
	resp = check("/api/users/"+strconv.Itoa(kidID)+"/my-summary?period=month", kidID, http.StatusOK)
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["summary"] != "" {
		t.Fatalf("unwritten month: got summary %q, want \"\"", out["summary"])
	}

	// A bad period is a 400, not a silent week.
	check("/api/users/"+strconv.Itoa(kidID)+"/my-summary?period=year", kidID, http.StatusBadRequest)
}

func TestPeriodRange_Week(t *testing.T) {
	tests := []struct {
		name      string
		ref       time.Time
		wantStart string
		wantEnd   string
	}{
		{
			name:      "Wednesday mid-week",
			ref:       time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC), // Wednesday
			wantStart: "2026-03-23",                                 // Monday
			wantEnd:   "2026-03-29",                                 // Sunday
		},
		{
			name:      "Monday (start of week)",
			ref:       time.Date(2026, 3, 23, 0, 0, 0, 0, time.UTC),
			wantStart: "2026-03-23",
			wantEnd:   "2026-03-29",
		},
		{
			name:      "Sunday (end of week)",
			ref:       time.Date(2026, 3, 29, 0, 0, 0, 0, time.UTC),
			wantStart: "2026-03-23",
			wantEnd:   "2026-03-29",
		},
		{
			name:      "Saturday",
			ref:       time.Date(2026, 3, 28, 0, 0, 0, 0, time.UTC),
			wantStart: "2026-03-23",
			wantEnd:   "2026-03-29",
		},
		{
			name:      "week spanning month boundary",
			ref:       time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), // Wednesday
			wantStart: "2026-03-30",                                // Monday
			wantEnd:   "2026-04-05",                                // Sunday
		},
		{
			name:      "week spanning year boundary",
			ref:       time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC), // Wednesday
			wantStart: "2025-12-29",                                  // Monday
			wantEnd:   "2026-01-04",                                  // Sunday
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end := periodRange("week", tt.ref)
			gotStart := start.Format("2006-01-02")
			gotEnd := end.Format("2006-01-02")
			if gotStart != tt.wantStart {
				t.Errorf("start = %s, want %s", gotStart, tt.wantStart)
			}
			if gotEnd != tt.wantEnd {
				t.Errorf("end = %s, want %s", gotEnd, tt.wantEnd)
			}
			// Verify start is Monday
			if start.Weekday() != time.Monday {
				t.Errorf("start weekday = %s, want Monday", start.Weekday())
			}
			// Verify end is Sunday
			if end.Weekday() != time.Sunday {
				t.Errorf("end weekday = %s, want Sunday", end.Weekday())
			}
		})
	}
}

func TestPeriodRange_Month(t *testing.T) {
	tests := []struct {
		name      string
		ref       time.Time
		wantStart string
		wantEnd   string
	}{
		{
			name:      "March (31 days)",
			ref:       time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC),
			wantStart: "2026-03-01",
			wantEnd:   "2026-03-31",
		},
		{
			name:      "April (30 days)",
			ref:       time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
			wantStart: "2026-04-01",
			wantEnd:   "2026-04-30",
		},
		{
			name:      "February non-leap year",
			ref:       time.Date(2025, 2, 14, 0, 0, 0, 0, time.UTC),
			wantStart: "2025-02-01",
			wantEnd:   "2025-02-28",
		},
		{
			name:      "February leap year",
			ref:       time.Date(2024, 2, 14, 0, 0, 0, 0, time.UTC),
			wantStart: "2024-02-01",
			wantEnd:   "2024-02-29",
		},
		{
			name:      "first day of month",
			ref:       time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			wantStart: "2026-01-01",
			wantEnd:   "2026-01-31",
		},
		{
			name:      "last day of month",
			ref:       time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC),
			wantStart: "2026-06-01",
			wantEnd:   "2026-06-30",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end := periodRange("month", tt.ref)
			gotStart := start.Format("2006-01-02")
			gotEnd := end.Format("2006-01-02")
			if gotStart != tt.wantStart {
				t.Errorf("start = %s, want %s", gotStart, tt.wantStart)
			}
			if gotEnd != tt.wantEnd {
				t.Errorf("end = %s, want %s", gotEnd, tt.wantEnd)
			}
		})
	}
}

func TestPeriodRange_Year(t *testing.T) {
	tests := []struct {
		name      string
		ref       time.Time
		wantStart string
		wantEnd   string
	}{
		{
			name:      "regular year",
			ref:       time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC),
			wantStart: "2026-01-01",
			wantEnd:   "2026-12-31",
		},
		{
			name:      "leap year",
			ref:       time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC),
			wantStart: "2024-01-01",
			wantEnd:   "2024-12-31",
		},
		{
			name:      "Jan 1",
			ref:       time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			wantStart: "2026-01-01",
			wantEnd:   "2026-12-31",
		},
		{
			name:      "Dec 31",
			ref:       time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC),
			wantStart: "2026-01-01",
			wantEnd:   "2026-12-31",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end := periodRange("year", tt.ref)
			gotStart := start.Format("2006-01-02")
			gotEnd := end.Format("2006-01-02")
			if gotStart != tt.wantStart {
				t.Errorf("start = %s, want %s", gotStart, tt.wantStart)
			}
			if gotEnd != tt.wantEnd {
				t.Errorf("end = %s, want %s", gotEnd, tt.wantEnd)
			}
		})
	}
}

func TestPeriodRange_Default(t *testing.T) {
	ref := time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC)
	start, end := periodRange("invalid", ref)
	if !start.Equal(ref) || !end.Equal(ref) {
		t.Errorf("default: got start=%s end=%s, want both %s", start, end, ref)
	}
}
