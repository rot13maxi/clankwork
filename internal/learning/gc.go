package learning

import (
	"context"
	"log/slog"
	"time"

	"github.com/rot13maxi/clankwork/internal/store"
)

// GCConfig holds tunable parameters for learning garbage collection.
type GCConfig struct {
	// MaxAgeDays is the maximum age (in days) before a learning becomes eligible
	// for GC. Learnings older than this that also have a low retention score
	// are deleted. Zero uses 30 days.
	MaxAgeDays int

	// MaxCount is the maximum number of non-source learnings to keep.
	// If the total exceeds this, the lowest-scoring ones are removed.
	// Zero uses 1000.
	MaxCount int

	// MinAccessCount is the minimum access_count threshold. Learnings with
	// access_count <= this value are candidates for GC when stale. Zero means
	// learnings that have never been accessed are eligible.
	MinAccessCount int
}

// GarbageCollector periodically prunes stale and low-value learnings.
type GarbageCollector struct {
	store  *store.Store
	config GCConfig
}

// NewGarbageCollector creates a new learning garbage collector.
func NewGarbageCollector(st *store.Store, cfg GCConfig) *GarbageCollector {
	if cfg.MaxAgeDays <= 0 {
		cfg.MaxAgeDays = 30
	}
	if cfg.MaxCount <= 0 {
		cfg.MaxCount = 1000
	}
	return &GarbageCollector{store: st, config: cfg}
}

// Run executes one GC cycle. It is designed to be called from a runLoop.
func (gc *GarbageCollector) Run(ctx context.Context) error {
	maxAge := time.Duration(gc.config.MaxAgeDays) * 24 * time.Hour

	// minScore: a learning with exactly MinAccessCount accesses and last accessed
	// at exactly maxAge would have a score of about (MinAccessCount+1) * exp(-decay * maxAge_days).
	// We use a small threshold to catch learnings with no or very few accesses.
	// With default halfLife=7 and maxAge=30 days:
	//   score = 1 * exp(-ln2/7 * 30) ≈ 0.051
	// We set minScore slightly higher so that learnings need at least some access
	// to survive past maxAge.
	minScore := float64(gc.config.MinAccessCount+1) * 0.1

	deleted, err := gc.store.LearningGC(ctx, maxAge, minScore, gc.config.MaxCount)
	if err != nil {
		return err
	}

	if deleted > 0 {
		slog.Info("learning gc: pruned learnings", "deleted", deleted,
			"max_age_days", gc.config.MaxAgeDays,
			"max_count", gc.config.MaxCount)
	}
	return nil
}
