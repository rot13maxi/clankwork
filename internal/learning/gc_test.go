package learning

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/rot13maxi/clankwork/internal/model"
	"github.com/rot13maxi/clankwork/internal/store"
)

func TestRetentionScore(t *testing.T) {
	now := time.Now().UTC()

	tests := []struct {
		name        string
		learning    model.Learning
		wantMin     float64
		wantMax     float64
		description string
	}{
		{
			name: "recently accessed, high count",
			learning: model.Learning{
				AccessCount:  10,
				CreatedAt:    now.Add(-30 * 24 * time.Hour),
				LastAccessed: timePtr(now.Add(-1 * time.Hour)),
			},
			wantMin: 9.0,
			wantMax: 11.0,
			description: "should score ~10 (accessCount * ~1.0 recency)",
		},
		{
			name: "never accessed, old",
			learning: model.Learning{
				AccessCount: 0,
				CreatedAt:   now.Add(-60 * 24 * time.Hour),
			},
			wantMin: 0.0,
			wantMax: 0.01,
			description: "should score near zero (1 * very low recency)",
		},
		{
			name: "accessed yesterday, low count",
			learning: model.Learning{
				AccessCount:  1,
				CreatedAt:    now.Add(-10 * 24 * time.Hour),
				LastAccessed: timePtr(now.Add(-24 * time.Hour)),
			},
			wantMin: 0.5,
			wantMax: 2.0,
			description: "should score moderately",
		},
		{
			name: "brand new, never accessed",
			learning: model.Learning{
				AccessCount: 0,
				CreatedAt:   now,
			},
			wantMin: 0.9,
			wantMax: 1.1,
			description: "brand new = high recency, count=1 baseline",
		},
		{
			name: "accessed 7 days ago (half life)",
			learning: model.Learning{
				AccessCount:  4,
				CreatedAt:    now.Add(-30 * 24 * time.Hour),
				LastAccessed: timePtr(now.Add(-7 * 24 * time.Hour)),
			},
			wantMin: 1.5,
			wantMax: 2.5,
			description: "should be roughly 4 * 0.5 = 2.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := store.RetentionScore(&tt.learning, now)
			if score < tt.wantMin || score > tt.wantMax {
				t.Errorf("%s: score = %f, want [%f, %f]", tt.description, score, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestRetentionScoreDecaysOverTime(t *testing.T) {
	now := time.Now().UTC()
	base := model.Learning{
		AccessCount: 5,
		CreatedAt:   now.Add(-90 * 24 * time.Hour),
	}

	// Score should decrease as last access gets older.
	var prevScore float64 = math.MaxFloat64
	for _, days := range []int{0, 1, 7, 14, 30, 60} {
		l := base
		accessed := now.Add(-time.Duration(days) * 24 * time.Hour)
		l.LastAccessed = &accessed
		score := store.RetentionScore(&l, now)
		if score >= prevScore {
			t.Errorf("score at %d days (%f) should be less than at previous interval (%f)", days, score, prevScore)
		}
		prevScore = score
	}
}

func TestLearningGC_DeletesStale(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	old := now.Add(-45 * 24 * time.Hour)

	// Create an old, never-accessed digest learning (should be GC'd).
	st.LearningCreate(ctx, "stale1", "test", "Old unused learning", "body")
	st.DB().ExecContext(ctx, `UPDATE learnings SET tier='digest', created_at=?, last_accessed=NULL, access_count=0 WHERE id=?`,
		old.Format(time.RFC3339), "stale1")

	// Create an old but frequently-accessed digest learning (should survive due to high score).
	st.LearningCreate(ctx, "active1", "test", "Old but active learning", "body")
	recentAccess := now.Add(-2 * 24 * time.Hour)
	st.DB().ExecContext(ctx, `UPDATE learnings SET tier='digest', created_at=?, last_accessed=?, access_count=50 WHERE id=?`,
		old.Format(time.RFC3339), recentAccess.Format(time.RFC3339), "active1")

	// Create a source learning (should never be GC'd even if old).
	st.LearningCreate(ctx, "source1", "test", "Source reference", "body")
	st.DB().ExecContext(ctx, `UPDATE learnings SET created_at=?, last_accessed=NULL, access_count=0 WHERE id=?`,
		old.Format(time.RFC3339), "source1")

	// Create a recent digest learning (should survive).
	st.LearningCreate(ctx, "recent1", "test", "Recent learning", "body")
	st.DB().ExecContext(ctx, `UPDATE learnings SET tier='digest' WHERE id=?`, "recent1")

	deleted, err := st.LearningGC(ctx, 30*24*time.Hour, 0.1, 1000)
	if err != nil {
		t.Fatal(err)
	}

	if deleted != 1 {
		t.Errorf("expected 1 deletion, got %d", deleted)
	}

	// Verify stale1 is gone.
	_, err = st.LearningGet(ctx, "stale1")
	if err == nil {
		t.Error("stale1 should have been deleted")
	}

	// Verify others survive.
	for _, id := range []string{"active1", "source1", "recent1"} {
		l, err := st.LearningGet(ctx, id)
		if err != nil || l == nil {
			t.Errorf("%s should have survived GC, got err: %v", id, err)
		}
	}
}

func TestLearningGC_EnforcesMaxCount(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC()

	// Create 5 digest learnings with varying scores.
	for i := 0; i < 5; i++ {
		id := string(rune('a'+i)) + "learn"
		st.LearningCreate(ctx, id, "test", "Learning "+id, "body")
		accessed := now.Add(-time.Duration(i*3) * 24 * time.Hour) // more recent = higher score
		st.DB().ExecContext(ctx, `UPDATE learnings SET tier='digest', access_count=?, last_accessed=? WHERE id=?`,
			i+1, accessed.Format(time.RFC3339), id)
	}

	// Also a source learning — should not count toward maxCount enforcement.
	st.LearningCreate(ctx, "src1", "test", "Source", "body")

	// GC with maxCount=2 — should delete 3 lowest-scoring digest learnings.
	deleted, err := st.LearningGC(ctx, 365*24*time.Hour, 0.0001, 2)
	if err != nil {
		t.Fatal(err)
	}

	if deleted != 3 {
		t.Errorf("expected 3 deletions for maxCount enforcement, got %d", deleted)
	}

	// Source should survive.
	l, err := st.LearningGet(ctx, "src1")
	if err != nil || l == nil {
		t.Error("source learning should survive maxCount enforcement")
	}
}

func TestLearningGC_NeverDeletesSource(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	old := time.Now().UTC().Add(-90 * 24 * time.Hour)

	// Create old source learnings with zero access.
	for i := 0; i < 3; i++ {
		id := string(rune('a'+i)) + "src"
		st.LearningCreate(ctx, id, "test", "Source "+id, "body")
		st.DB().ExecContext(ctx, `UPDATE learnings SET created_at=?, access_count=0, last_accessed=NULL WHERE id=?`,
			old.Format(time.RFC3339), id)
	}

	deleted, err := st.LearningGC(ctx, 30*24*time.Hour, 1.0, 1)
	if err != nil {
		t.Fatal(err)
	}

	if deleted != 0 {
		t.Errorf("should not delete source learnings, deleted %d", deleted)
	}
}

func TestLearningGC_NoOpWhenEmpty(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	deleted, err := st.LearningGC(ctx, 30*24*time.Hour, 0.1, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 0 {
		t.Errorf("expected 0 deletions on empty store, got %d", deleted)
	}
}

func TestGarbageCollectorRun(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	old := time.Now().UTC().Add(-60 * 24 * time.Hour)

	// Create old stale digest learning.
	st.LearningCreate(ctx, "gc-target", "test", "Old stale", "body")
	st.DB().ExecContext(ctx, `UPDATE learnings SET tier='digest', created_at=?, access_count=0, last_accessed=NULL WHERE id=?`,
		old.Format(time.RFC3339), "gc-target")

	gc := NewGarbageCollector(st, GCConfig{
		MaxAgeDays: 30,
		MaxCount:   1000,
	})

	if err := gc.Run(ctx); err != nil {
		t.Fatal(err)
	}

	_, err := st.LearningGet(ctx, "gc-target")
	if err == nil {
		t.Error("gc-target should have been deleted by GarbageCollector.Run")
	}
}

func TestGarbageCollectorDefaults(t *testing.T) {
	gc := NewGarbageCollector(nil, GCConfig{})
	if gc.config.MaxAgeDays != 30 {
		t.Errorf("default MaxAgeDays = %d, want 30", gc.config.MaxAgeDays)
	}
	if gc.config.MaxCount != 1000 {
		t.Errorf("default MaxCount = %d, want 1000", gc.config.MaxCount)
	}
}

func timePtr(t time.Time) *time.Time {
	return &t
}
