package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/liftedkilt/openchore/internal/model"
)

// Grandparent boost links (P4-16). A link is an outward share with one
// fixed token: the elder's only credential. The caps live on the row —
// a monthly allowance and a per-day limit — and the boost lands in the
// ledger (reason='grandparent_boost') with the link as reference_id, so
// every gifted point stays auditable.

// ErrBoostLinkNotFound means the token matched nothing alive (missing,
// revoked or expired).
var ErrBoostLinkNotFound = errors.New("boost link not found")

// ErrBoostCapReached means the monthly allowance is spent.
var ErrBoostCapReached = errors.New("monthly boost cap reached")

// ErrBoostDailyLimit means the link already boosted its daily share.
var ErrBoostDailyLimit = errors.New("daily boost limit reached")

func scanBoostLink(row interface{ Scan(...any) error }) (*model.BoostLink, error) {
	l := &model.BoostLink{}
	var lastBoost sql.NullTime
	if err := row.Scan(&l.ID, &l.UserID, &l.Token, &l.Label, &l.MonthlyCap,
		&l.DailyLimit, &l.Granted, &l.GrantedMonth, &l.Revoked, &l.ExpiresAt,
		&lastBoost, &l.CreatedAt); err != nil {
		return nil, err
	}
	if lastBoost.Valid {
		l.LastBoostAt = &lastBoost.Time
	}
	return l, nil
}

const boostLinkColumns = `id, user_id, token, label, monthly_cap, daily_limit, granted, granted_month, revoked, expires_at, last_boost_at, created_at`

// CreateBoostLink generates a fresh token-addressed link for a kid.
// monthlyCap/dailyLimit fall back to the defaults when unset.
func (s *Store) CreateBoostLink(ctx context.Context, userID int64, label string, monthlyCap, dailyLimit, days int) (*model.BoostLink, error) {
	if monthlyCap <= 0 {
		monthlyCap = model.BoostDefaultMonthlyCap
	}
	if dailyLimit <= 0 {
		dailyLimit = model.BoostDefaultDailyLimit
	}
	if days <= 0 {
		days = model.BoostDefaultDays
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(raw)
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO boost_links (user_id, token, label, monthly_cap, daily_limit, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		userID, token, label, monthlyCap, dailyLimit,
		time.Now().AddDate(0, 0, days).UTC()); err != nil {
		return nil, err
	}
	return s.GetBoostLinkByToken(ctx, token)
}

// ListBoostLinks returns a kid's links, newest first. The token is
// cleared before handing the rows over: sharing is a create-time event,
// and the management list must never re-leak it.
func (s *Store) ListBoostLinks(ctx context.Context, userID int64) ([]model.BoostLink, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+boostLinkColumns+` FROM boost_links WHERE user_id = ? ORDER BY created_at DESC, id DESC`,
		userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var links []model.BoostLink
	for rows.Next() {
		l, err := scanBoostLink(rows)
		if err != nil {
			return nil, err
		}
		l.Token = ""
		links = append(links, *l)
	}
	return links, rows.Err()
}

// GetBoostLinkByToken resolves a token to its link; the token must be
// alive (not revoked, not expired) for a lookup the elder can use.
func (s *Store) GetBoostLinkByToken(ctx context.Context, token string) (*model.BoostLink, error) {
	l, err := scanBoostLink(s.db.QueryRowContext(ctx,
		`SELECT `+boostLinkColumns+` FROM boost_links WHERE token = ?`, token))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrBoostLinkNotFound
	}
	if err != nil {
		return nil, err
	}
	if l.Revoked || !time.Now().UTC().Before(l.ExpiresAt) {
		return nil, ErrBoostLinkNotFound
	}
	return l, nil
}

// GetBoostLink reloads a link by id — the post-boost snapshot wants the
// row as it stands now (the caller's copy is stale the moment the boost
// moves granted and last_boost_at).
func (s *Store) GetBoostLink(ctx context.Context, id int64) (*model.BoostLink, error) {
	l, err := scanBoostLink(s.db.QueryRowContext(ctx,
		`SELECT `+boostLinkColumns+` FROM boost_links WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrBoostLinkNotFound
	}
	return l, err
}

// RevokeBoostLink kills a link's token. Idempotent.
func (s *Store) RevokeBoostLink(ctx context.Context, id, userID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE boost_links SET revoked = 1 WHERE id = ? AND user_id = ?`, id, userID)
	return err
}

// BoostToday counts today's boosts on a link — the per-day guard.
func (s *Store) BoostToday(ctx context.Context, linkID int64) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM point_transactions
		 WHERE reason = ? AND reference_id = ? AND DATE(created_at, 'localtime') = DATE('now', 'localtime')`,
		model.ReasonGrandparentBoost, linkID).Scan(&count)
	return count, err
}

// ApplyBoost credits the elder's gift under the caps, in one transaction:
// the ledger row (with the link as reference_id and the elder's label in
// the note), the month's granted tally and the link's last_boost_at all
// move together. The month rolls over lazily — the first boost of a new
// calendar month resets the tally.
//
// The today-check binds the day as a plain "YYYY-MM-DD" string. Binding
// a time.Time would hit SQLite's parser as Go's default time format
// ("… +0800 CST m=…"), which date() can't read — and the created_at
// column is stored UTC, so the comparison needs 'localtime' to land on
// the household's day.
func (s *Store) ApplyBoost(ctx context.Context, link *model.BoostLink, amount int, now time.Time) error {
	if amount <= 0 {
		return errors.New("amount must be positive")
	}
	month := now.Format("2006-01")
	today := now.Format("2006-01-02")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var granted, monthlyCap int
	var grantedMonth string
	if err := tx.QueryRowContext(ctx,
		`SELECT granted, monthly_cap, granted_month FROM boost_links WHERE id = ?`,
		link.ID).Scan(&granted, &monthlyCap, &grantedMonth); err != nil {
		return err
	}
	// A new calendar month opens a fresh allowance.
	if grantedMonth != month {
		granted = 0
	}
	if granted+amount > monthlyCap {
		return ErrBoostCapReached
	}

	var todayCount int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM point_transactions
		 WHERE reason = ? AND reference_id = ? AND DATE(created_at, 'localtime') = ?`,
		model.ReasonGrandparentBoost, link.ID, today).Scan(&todayCount); err != nil {
		return err
	}
	if todayCount >= link.DailyLimit {
		return ErrBoostDailyLimit
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO point_transactions (user_id, amount, reason, reference_id, note)
		 VALUES (?, ?, ?, ?, ?)`,
		link.UserID, amount, model.ReasonGrandparentBoost, link.ID,
		"Boost: "+link.Label); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE boost_links SET granted = ?, granted_month = ?, last_boost_at = ? WHERE id = ?`,
		granted+amount, month, now.UTC(), link.ID); err != nil {
		return err
	}
	return tx.Commit()
}
