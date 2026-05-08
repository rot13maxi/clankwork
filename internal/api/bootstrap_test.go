package api

import (
	"strings"
	"testing"

	"github.com/rot13maxi/clankwork/internal/model"
)

func TestBuildLearningQuery(t *testing.T) {
	tests := []struct {
		name        string
		title       string
		body        string
		wantEmpty   bool
		wantTerms   []string
		wantAbsent  []string
		wantOR      bool
	}{
		{
			name:      "empty input",
			wantEmpty: true,
		},
		{
			name:      "all special chars",
			title:     "--- *** ///",
			body:      "!@#$%^&*()",
			wantEmpty: true,
		},
		{
			name:      "short tokens filtered",
			title:     "a bb ccc dddd",
			wantTerms: []string{"ccc", "dddd"},
			wantAbsent: []string{"a", "bb"},
		},
		{
			name:      "path separators stripped",
			title:     "internal/config package",
			wantTerms: []string{"internal", "config", "package"},
		},
		{
			name:    "deduplication",
			title:   "fix fix fix the bug bug",
			wantTerms: []string{"fix", "the", "bug"},
		},
		{
			name:    "OR semantics",
			title:   "database migration",
			wantOR:  true,
		},
		{
			name:      "newlines and tabs stripped",
			title:     "fix\nthe\tthing",
			wantTerms: []string{"fix", "the", "thing"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildLearningQuery(tt.title, tt.body)

			if tt.wantEmpty {
				if got != "" {
					t.Errorf("want empty, got %q", got)
				}
				return
			}

			for _, term := range tt.wantTerms {
				if !strings.Contains(got, term) {
					t.Errorf("want term %q in query %q", term, got)
				}
			}
			for _, term := range tt.wantAbsent {
				if strings.Contains(got, term) {
					t.Errorf("want term %q absent from query %q", term, got)
				}
			}
			if tt.wantOR {
				terms := strings.Fields(got)
				hasOR := false
				for _, t := range terms {
					if t == "OR" {
						hasOR = true
						break
					}
				}
				if !hasOR && len(strings.Fields(got)) > 1 {
					t.Errorf("multi-term query should use OR, got %q", got)
				}
			}
		})
	}
}

func TestFilterLearningsByTier(t *testing.T) {
	mkLearning := func(id, tier, body string) *model.Learning {
		return &model.Learning{ID: id, Tier: tier, Title: "title-" + id, Body: body}
	}

	t.Run("orders by tier and caps", func(t *testing.T) {
		all := []*model.Learning{
			mkLearning("s1", "source", "full body 1"),
			mkLearning("s2", "source", "full body 2"),
			mkLearning("d1", "digest", "digest body 1"),
			mkLearning("d2", "digest", "digest body 2"),
			mkLearning("d3", "digest", "digest body 3"),
			mkLearning("d4", "digest", "digest body 4"),
			mkLearning("i1", "index", "index body 1"),
			mkLearning("i2", "index", "index body 2"),
			mkLearning("i3", "index", "index body 3"),
			mkLearning("i4", "index", "index body 4"),
			mkLearning("i5", "index", "index body 5"),
			mkLearning("i6", "index", "index body 6"),
		}
		result := filterLearningsByTier(all)

		// Should have 5 index + 3 digest + 1 source = 9
		if len(result) != 9 {
			t.Fatalf("want 9 learnings, got %d", len(result))
		}

		// First 5 should be index tier with empty bodies
		for i := 0; i < 5; i++ {
			if result[i].Tier != "index" {
				t.Errorf("result[%d].Tier = %q, want index", i, result[i].Tier)
			}
			if result[i].Body != "" {
				t.Errorf("index tier result[%d].Body should be empty, got %q", i, result[i].Body)
			}
		}

		// Next 3 should be digest
		for i := 5; i < 8; i++ {
			if result[i].Tier != "digest" {
				t.Errorf("result[%d].Tier = %q, want digest", i, result[i].Tier)
			}
			if result[i].Body == "" {
				t.Errorf("digest tier result[%d].Body should not be empty", i)
			}
		}

		// Last 1 should be source
		if result[8].Tier != "source" {
			t.Errorf("result[8].Tier = %q, want source", result[8].Tier)
		}
	})

	t.Run("empty input", func(t *testing.T) {
		result := filterLearningsByTier(nil)
		if len(result) != 0 {
			t.Errorf("want 0 learnings, got %d", len(result))
		}
	})

	t.Run("only source learnings", func(t *testing.T) {
		all := []*model.Learning{
			mkLearning("s1", "source", "body1"),
			mkLearning("s2", "source", "body2"),
			mkLearning("s3", "source", "body3"),
		}
		result := filterLearningsByTier(all)
		if len(result) != 1 {
			t.Fatalf("want 1 source learning, got %d", len(result))
		}
		if result[0].ID != "s1" {
			t.Errorf("want first source learning, got %q", result[0].ID)
		}
	})
}

func TestBuildLearningQueryTruncation(t *testing.T) {
	// Input longer than 200 chars should be truncated, not panic.
	long := strings.Repeat("word ", 100) // 500 chars
	got := buildLearningQuery(long, "")
	if got == "" {
		t.Error("want non-empty query for long input")
	}
	// All tokens should be valid words (no special chars).
	for _, tok := range strings.Fields(got) {
		if tok == "OR" {
			continue
		}
		for _, ch := range tok {
			if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9')) {
				t.Errorf("token %q contains non-alphanumeric char %q", tok, ch)
			}
		}
	}
}
