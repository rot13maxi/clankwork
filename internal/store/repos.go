package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/rot13maxi/clankwork/internal/model"
)

func (s *Store) RepoCreate(ctx context.Context, id, name, path, targetBranch, verifyCommand, lintCommand, typecheckCommand string, autoPush bool) (*model.Repo, error) {
	now := time.Now().UTC()
	autoPushInt := 0
	if autoPush {
		autoPushInt = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO repos (id, name, path, target_branch, verify_command, lint_command, typecheck_command, auto_push, created_at)
		 VALUES (?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?)`,
		id, name, path, targetBranch, verifyCommand, lintCommand, typecheckCommand, autoPushInt, now.Format(time.RFC3339),
	)
	if err != nil {
		return nil, fmt.Errorf("insert repo: %w", err)
	}
	return s.RepoGet(ctx, id)
}

const repoColumns = `id, name, path, target_branch, COALESCE(verify_command,''), COALESCE(lint_command,''), COALESCE(typecheck_command,''), COALESCE(auto_push,0), created_at`

func (s *Store) RepoGet(ctx context.Context, id string) (*model.Repo, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+repoColumns+` FROM repos WHERE id = ?`, id)
	r, err := scanRepo(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("repo %q not found", id)
	}
	return r, err
}

func (s *Store) RepoGetByName(ctx context.Context, name string) (*model.Repo, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+repoColumns+` FROM repos WHERE name = ?`, name)
	r, err := scanRepo(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("repo %q not found", name)
	}
	return r, err
}

func (s *Store) RepoList(ctx context.Context) ([]*model.Repo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+repoColumns+` FROM repos ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var repos []*model.Repo
	for rows.Next() {
		r, err := scanRepo(rows)
		if err != nil {
			return nil, err
		}
		repos = append(repos, r)
	}
	return repos, rows.Err()
}

type repoScanner interface {
	Scan(dest ...any) error
}

func scanRepo(row repoScanner) (*model.Repo, error) {
	var r model.Repo
	var createdAt string
	var autoPushInt int
	if err := row.Scan(&r.ID, &r.Name, &r.Path, &r.TargetBranch, &r.VerifyCommand, &r.LintCommand, &r.TypecheckCommand, &autoPushInt, &createdAt); err != nil {
		return nil, err
	}
	r.AutoPush = autoPushInt != 0
	r.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	return &r, nil
}

func (s *Store) RepoCount(ctx context.Context) (int, error) {
	var n int
	row := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM repos`)
	return n, row.Scan(&n)
}
