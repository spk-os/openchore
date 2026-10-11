package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/liftedkilt/openchore/internal/model"
)

// The kid's pet (P4-13). Storage is deliberately thin: the pet row holds
// only what persists (name, stage, fed_value, timestamps); the mood is
// computed from the kid's chore record at read time so it can never drift
// from reality. Feeding debits the ledger (reason='pet_feed') in the same
// transaction as the fed_value bump.

// ErrInsufficientPoints means the kid's spendable balance can't cover a
// feeding. Surfaces as 422 so the UI can say "not enough points".
var ErrInsufficientPoints = errors.New("insufficient points")

// GetPet returns the kid's pet, or nil when they haven't hatched one yet.
func (s *Store) GetPet(ctx context.Context, userID int64) (*model.Pet, error) {
	p := &model.Pet{}
	var lastFed sql.NullTime
	err := s.db.QueryRowContext(ctx,
		`SELECT id, user_id, name, stage, fed_value, last_fed_at, hatched_at FROM pets WHERE user_id = ?`,
		userID).
		Scan(&p.ID, &p.UserID, &p.Name, &p.Stage, &p.FedValue, &lastFed, &p.HatchedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if lastFed.Valid {
		p.LastFedAt = &lastFed.Time
	}
	return p, nil
}

// SavePetName names (or renames) the kid's pet, hatching it first if
// needed. Hatching starts at stage 1 with fed_value 0.
func (s *Store) SavePetName(ctx context.Context, userID int64, name string) (*model.Pet, error) {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO pets (user_id, name) VALUES (?, ?)
		 ON CONFLICT (user_id) DO UPDATE SET name = excluded.name`,
		userID, name); err != nil {
		return nil, err
	}
	return s.GetPet(ctx, userID)
}

// FeedPet spends points on pet food: one ledger debit (reason='pet_feed')
// and the fed_value bump happen in one transaction, and the stage is
// promoted when the cumulative value crosses the thresholds. Returns the
// updated pet.
func (s *Store) FeedPet(ctx context.Context, userID int64, cost int) (*model.Pet, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Check the spendable balance first — SUM(amount) is the same
	// spendable figure the points endpoint reports.
	var balance int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(amount), 0) FROM point_transactions WHERE user_id = ?`,
		userID).Scan(&balance); err != nil {
		return nil, err
	}
	if balance < cost {
		return nil, ErrInsufficientPoints
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO point_transactions (user_id, amount, reason, note)
		 VALUES (?, ?, ?, 'Pet food')`,
		userID, -cost, model.ReasonPetFeed); err != nil {
		return nil, err
	}

	var fedValue int
	if err := tx.QueryRowContext(ctx,
		`UPDATE pets SET fed_value = fed_value + ?, last_fed_at = CURRENT_TIMESTAMP
		 WHERE user_id = ? RETURNING fed_value`,
		cost, userID).Scan(&fedValue); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("no pet to feed")
		}
		return nil, err
	}

	stage := 1
	if fedValue >= model.PetStage3FedValue {
		stage = 3
	} else if fedValue >= model.PetStage2FedValue {
		stage = 2
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE pets SET stage = ? WHERE user_id = ?`, stage, userID); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetPet(ctx, userID)
}

// PetMood computes the pet's mood from the kid's recent chore record:
//
//   - happy:  today's required chores are all done (or none were scheduled)
//   - calm:   some required chores are still open
//   - hungry: required chores were missed two days in a row
//
// The pet never starves for a day that simply had nothing scheduled, and
// pending (awaiting approval) completions already count as done — the pet
// trusts the kid.
func (s *Store) PetMood(ctx context.Context, userID int64, today string) (string, error) {
	// Two-day lookback for the hungry check.
	dates := make([]string, 0, 2)
	if d, err := time.Parse(model.DateFormat, today); err == nil {
		dates = append(dates, d.AddDate(0, 0, -1).Format(model.DateFormat))
	}
	dates = append(dates, today)

	now := time.Now()
	scheduled, err := s.GetScheduledChoresForUser(ctx, userID, dates, now)
	if err != nil {
		return "", err
	}
	byDate := map[string][]model.ScheduledChore{}
	for _, c := range scheduled {
		byDate[normalizeDate(c.Date)] = append(byDate[normalizeDate(c.Date)], c)
	}

	requiredIncomplete := func(date string) bool {
		for _, c := range byDate[date] {
			if c.Category == model.CategoryRequired && !c.Completed {
				return true
			}
		}
		return false
	}

	yesterday := ""
	if d, err := time.Parse(model.DateFormat, today); err == nil {
		yesterday = d.AddDate(0, 0, -1).Format(model.DateFormat)
	}
	if requiredIncomplete(yesterday) && requiredIncomplete(today) {
		return model.PetMoodHungry, nil
	}
	if requiredIncomplete(today) {
		return model.PetMoodCalm, nil
	}
	return model.PetMoodHappy, nil
}
