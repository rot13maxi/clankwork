package store

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/rot13maxi/clankwork/internal/model"
)

// scoredLearning pairs a learning ID with its computed retention score.
type scoredLearning struct {
	id    string
	score float64
	stale bool // exceeds maxAge AND below minScore
}

// RetentionScore computes an access-weighted retention score for a learning.
// Higher scores mean the learning is more valuable and should be kept.
// Formula: accessCount * exp(-daysSinceLastAccess / halfLifeDays)
// where halfLifeDays controls how fast recency decays (default 7 days).
func RetentionScore(l *model.Learning, now time.Time) float64 {
	const halfLifeDays = 7.0
	const decayRate = math.Ln2 / halfLifeDays

	// If never accessed, use creation time as last access.
	lastAccess := l.CreatedAt
	if l.LastAccessed != nil {
		lastAccess = *l.LastAccessed
	}

	daysSinceAccess := now.Sub(lastAccess).Hours() / 24.0
	if daysSinceAccess < 0 {
		daysSinceAccess = 0
	}

	recencyWeight := math.Exp(-decayRate * daysSinceAccess)

	// Ensure a minimum score of 1 for access count so brand-new learnings
	// get some base score from recency alone.
	count := float64(l.AccessCount)
	if count < 1 {
		count = 1
	}

	return count * recencyWeight
}

func (s *Store) LearningCreate(ctx context.Context, id, category, title, body string) (*model.Learning, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO learnings (id, category, title, body, tier, created_at, access_count)
		 VALUES (?, ?, ?, ?, 'source', ?, 0)`,
		id, category, title, body, now,
	)
	if err != nil {
		return nil, fmt.Errorf("insert learning: %w", err)
	}
	return s.LearningGet(ctx, id)
}

func (s *Store) LearningGet(ctx context.Context, id string) (*model.Learning, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, category, title, body, tier, created_at, COALESCE(last_accessed,''), access_count
		 FROM learnings WHERE id = ?`, id)
	return scanLearning(row)
}

func (s *Store) LearningList(ctx context.Context, category string, limit int) ([]*model.Learning, error) {
	q := `SELECT id, category, title, body, tier, created_at, COALESCE(last_accessed,''), access_count
		  FROM learnings`
	var args []any
	if category != "" {
		q += ` WHERE category = ?`
		args = append(args, category)
	}
	q += ` ORDER BY access_count DESC, created_at DESC`
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ls []*model.Learning
	for rows.Next() {
		l, err := scanLearning(rows)
		if err != nil {
			return nil, err
		}
		ls = append(ls, l)
	}
	return ls, rows.Err()
}

// LearningSearch returns up to limit learnings matching query via FTS5.
// Falls back to LearningList if query is empty.
func (s *Store) LearningSearch(ctx context.Context, query string, limit int) ([]*model.Learning, error) {
	if query == "" {
		return s.LearningList(ctx, "", limit)
	}
	q := `SELECT l.id, l.category, l.title, l.body, l.tier, l.created_at,
	             COALESCE(l.last_accessed,''), l.access_count
	      FROM learnings l
	      JOIN learnings_fts f ON l.rowid = f.rowid
	      WHERE learnings_fts MATCH ?
	      ORDER BY rank
	      LIMIT ?`
	rows, err := s.db.QueryContext(ctx, q, query, limit)
	if err != nil {
		return s.LearningList(ctx, "", limit) // graceful fallback
	}
	defer rows.Close()
	var ls []*model.Learning
	for rows.Next() {
		l, err := scanLearning(rows)
		if err != nil {
			return nil, err
		}
		ls = append(ls, l)
	}
	return ls, rows.Err()
}

// LearningBumpAccess updates last_accessed and increments access_count for the given IDs.
func (s *Store) LearningBumpAccess(ctx context.Context, ids []string) {
	if len(ids) == 0 {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, id := range ids {
		s.db.ExecContext(ctx,
			`UPDATE learnings SET last_accessed=?, access_count=access_count+1 WHERE id=?`,
			now, id)
	}
}

func (s *Store) CandidateLearningCreate(ctx context.Context, req model.AddCandidateLearningRequest) (*model.CandidateLearning, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	candidate := &model.CandidateLearning{
		ID:               ulid.Make().String(),
		SourceTraceID:    req.SourceTraceID,
		ProposedLearning: req.ProposedLearning,
		Reason:           req.Reason,
		Status:           "candidate",
		CreatedAt:        now,
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO candidate_learnings (id, source_trace_id, proposed_learning, reason, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		candidate.ID, candidate.SourceTraceID, candidate.ProposedLearning, candidate.Reason, candidate.Status, candidate.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("insert candidate learning: %w", err)
	}
	return candidate, nil
}

func (s *Store) CandidateLearningList(ctx context.Context, status string, limit int) ([]*model.CandidateLearning, error) {
	q := `SELECT id, source_trace_id, proposed_learning, reason, status, created_at, COALESCE(reviewed_at, '')
	      FROM candidate_learnings`
	var args []any
	if status != "" {
		q += ` WHERE status = ?`
		args = append(args, status)
	}
	q += ` ORDER BY created_at DESC`
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var candidates []*model.CandidateLearning
	for rows.Next() {
		candidate, err := scanCandidateLearning(rows)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

type learningScanner interface {
	Scan(dest ...any) error
}

func scanLearning(row learningScanner) (*model.Learning, error) {
	var l model.Learning
	var createdAt, lastAccessed string
	if err := row.Scan(&l.ID, &l.Category, &l.Title, &l.Body, &l.Tier, &createdAt, &lastAccessed, &l.AccessCount); err != nil {
		return nil, err
	}
	l.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	if lastAccessed != "" {
		ts, _ := time.Parse(time.RFC3339, lastAccessed)
		l.LastAccessed = &ts
	}
	return &l, nil
}

func scanCandidateLearning(row learningScanner) (*model.CandidateLearning, error) {
	var c model.CandidateLearning
	if err := row.Scan(&c.ID, &c.SourceTraceID, &c.ProposedLearning, &c.Reason, &c.Status, &c.CreatedAt, &c.ReviewedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, err
		}
		return nil, err
	}
	return &c, nil
}

// LearningGC performs garbage collection on learnings.
//
// It deletes learnings that meet ALL of:
//   - tier is NOT "source" (source learnings are reference material and never GC'd)
//   - last accessed (or created, if never accessed) is older than maxAge
//   - retention score is below minScore
//
// Additionally, if the total count of non-source learnings exceeds maxCount,
// the lowest-scoring ones are deleted until the count is at or below maxCount.
//
// Returns the number of deleted learnings.
func (s *Store) LearningGC(ctx context.Context, maxAge time.Duration, minScore float64, maxCount int) (int, error) {
	now := time.Now().UTC()

	// Load all non-source learnings.
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, category, title, body, tier, created_at, COALESCE(last_accessed,''), access_count
		 FROM learnings WHERE tier != 'source'
		 ORDER BY created_at ASC`)
	if err != nil {
		return 0, fmt.Errorf("learning gc query: %w", err)
	}
	defer rows.Close()

	var all []scoredLearning
	cutoff := now.Add(-maxAge)
	for rows.Next() {
		l, err := scanLearning(rows)
		if err != nil {
			return 0, fmt.Errorf("learning gc scan: %w", err)
		}
		sc := RetentionScore(l, now)

		lastAccess := l.CreatedAt
		if l.LastAccessed != nil {
			lastAccess = *l.LastAccessed
		}

		all = append(all, scoredLearning{
			id:    l.ID,
			score: sc,
			stale: lastAccess.Before(cutoff) && sc < minScore,
		})
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("learning gc rows: %w", err)
	}

	// Phase 1: delete stale learnings (old + low score).
	deleteIDs := make(map[string]bool)
	for _, item := range all {
		if item.stale {
			deleteIDs[item.id] = true
		}
	}

	// Phase 2: enforce maxCount on remaining learnings.
	// Build list of survivors sorted by score ascending.
	var survivors []scoredLearning
	for _, item := range all {
		if !deleteIDs[item.id] {
			survivors = append(survivors, item)
		}
	}

	if maxCount > 0 && len(survivors) > maxCount {
		// Sort by score ascending so we can delete the lowest.
		sort.Slice(survivors, func(i, j int) bool {
			return survivors[i].score < survivors[j].score
		})
		excess := len(survivors) - maxCount
		for i := 0; i < excess; i++ {
			deleteIDs[survivors[i].id] = true
		}
	}

	if len(deleteIDs) == 0 {
		return 0, nil
	}

	// Delete in batches.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("learning gc begin tx: %w", err)
	}
	defer tx.Rollback()

	deleted := 0
	for id := range deleteIDs {
		res, err := tx.ExecContext(ctx, `DELETE FROM learnings WHERE id = ?`, id)
		if err != nil {
			return deleted, fmt.Errorf("learning gc delete: %w", err)
		}
		n, _ := res.RowsAffected()
		deleted += int(n)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("learning gc commit: %w", err)
	}

	return deleted, nil
}
