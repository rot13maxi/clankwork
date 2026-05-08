package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/rot13maxi/clankwork/internal/model"
)

func (s *Store) PlanCreate(ctx context.Context, id, title, path string) (*model.Plan, error) {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO plans (id, title, status, path, created_at, updated_at) VALUES (?, ?, 'active', ?, ?, ?)`,
		id, title, path, now.Format(time.RFC3339), now.Format(time.RFC3339),
	)
	if err != nil {
		return nil, fmt.Errorf("insert plan: %w", err)
	}
	return s.PlanGet(ctx, id)
}

func (s *Store) PlanGet(ctx context.Context, id string) (*model.Plan, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, title, status, path, created_at, updated_at FROM plans WHERE id = ?`, id)
	p, err := scanPlan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("plan %q not found", id)
	}
	return p, err
}

func (s *Store) PlanList(ctx context.Context) ([]*model.Plan, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, title, status, path, created_at, updated_at FROM plans ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var plans []*model.Plan
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		plans = append(plans, p)
	}
	return plans, rows.Err()
}

type planScanner interface {
	Scan(dest ...any) error
}

func scanPlan(row planScanner) (*model.Plan, error) {
	var p model.Plan
	var createdAt, updatedAt string
	if err := row.Scan(&p.ID, &p.Title, &p.Status, &p.Path, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	p.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	p.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return &p, nil
}

func (s *Store) PlanStats(ctx context.Context) (total, active int, err error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), SUM(CASE WHEN status='active' THEN 1 ELSE 0 END) FROM plans`)
	err = row.Scan(&total, &active)
	return
}
