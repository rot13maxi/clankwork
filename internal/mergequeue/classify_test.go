package mergequeue

import (
	"context"
	"strings"
	"testing"
)

func TestClassifyConflict_LockFile(t *testing.T) {
	log := "CONFLICT (content): Merge conflict in go.sum\nUU go.sum"
	a := ClassifyConflict(log)
	if a.Class != ConflictTrivial {
		t.Errorf("go.sum conflict: got %q, want trivial", a.Class)
	}
	if len(a.Files) != 1 || a.Files[0] != "go.sum" {
		t.Errorf("files = %v, want [go.sum]", a.Files)
	}
}

func TestClassifyConflict_PackageLockJSON(t *testing.T) {
	log := "UU package-lock.json"
	a := ClassifyConflict(log)
	if a.Class != ConflictTrivial {
		t.Errorf("package-lock.json conflict: got %q, want trivial", a.Class)
	}
}

func TestClassifyConflict_GeneratedPB(t *testing.T) {
	log := "UU api/v1/service.pb.go"
	a := ClassifyConflict(log)
	if a.Class != ConflictTrivial {
		t.Errorf("protobuf generated file: got %q, want trivial", a.Class)
	}
	if a.Details[0].Reason != "generated file" {
		t.Errorf("reason = %q, want generated file", a.Details[0].Reason)
	}
}

func TestClassifyConflict_MockFile(t *testing.T) {
	log := "UU internal/service/mock_repository.go"
	a := ClassifyConflict(log)
	if a.Class != ConflictTrivial {
		t.Errorf("mock file: got %q, want trivial", a.Class)
	}
}

func TestClassifyConflict_TestFile(t *testing.T) {
	log := "UU internal/auth/handler_test.go"
	a := ClassifyConflict(log)
	if a.Class != ConflictSemantic {
		t.Errorf("test file conflict: got %q, want semantic", a.Class)
	}
	if !strings.Contains(a.Details[0].Reason, "test") {
		t.Errorf("reason should mention test, got %q", a.Details[0].Reason)
	}
}

func TestClassifyConflict_JSTestFile(t *testing.T) {
	log := "UU src/components/Button.test.tsx"
	a := ClassifyConflict(log)
	if a.Class != ConflictSemantic {
		t.Errorf("JS test file: got %q, want semantic", a.Class)
	}
}

func TestClassifyConflict_MigrationFile(t *testing.T) {
	log := "UU db/migrations/20240101_add_column.sql"
	a := ClassifyConflict(log)
	if a.Class != ConflictSemantic {
		t.Errorf("migration file: got %q, want semantic", a.Class)
	}
	if !strings.Contains(a.Details[0].Reason, "migration") {
		t.Errorf("reason should mention migration, got %q", a.Details[0].Reason)
	}
}

func TestClassifyConflict_SQLFile(t *testing.T) {
	log := "UU schema/users.sql"
	a := ClassifyConflict(log)
	if a.Class != ConflictSemantic {
		t.Errorf("SQL file: got %q, want semantic", a.Class)
	}
}

func TestClassifyConflict_ImportConflict(t *testing.T) {
	log := `CONFLICT (content): Merge conflict in main.go
UU main.go
--- FILE: main.go ---
package main

<<<<<<< HEAD
import "fmt"
import "os"
=======
import "fmt"
import "net/http"
>>>>>>> feature

func main() {}
`
	a := ClassifyConflict(log)
	if a.Class != ConflictTrivial {
		t.Errorf("import conflict: got %q, want trivial", a.Class)
	}
	if a.Details[0].Markers != 1 {
		t.Errorf("markers = %d, want 1", a.Details[0].Markers)
	}
}

func TestClassifyConflict_FunctionBodyConflict(t *testing.T) {
	log := `UU internal/server/handler.go
--- FILE: internal/server/handler.go ---
func handleRequest(ctx context.Context, r *http.Request) {
<<<<<<< HEAD
	err := validateInput(ctx, r)
	if err != nil {
		return fmt.Errorf("validation: %w", err)
	}
	result, err := processV2(ctx, r)
=======
	data, err := parseRequest(ctx, r)
	if err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	result, err := processV1(ctx, data)
>>>>>>> feature
}
`
	a := ClassifyConflict(log)
	if a.Class != ConflictSemantic {
		t.Errorf("function body conflict: got %q, want semantic", a.Class)
	}
	if !strings.Contains(a.Details[0].Reason, "function body") {
		t.Errorf("reason should mention function body, got %q", a.Details[0].Reason)
	}
}

func TestClassifyConflict_DeleteVsModify(t *testing.T) {
	log := `UU internal/service/auth.go
--- FILE: internal/service/auth.go ---
<<<<<<< HEAD
=======
func validateToken(ctx context.Context, token string) error {
	return checkExpiry(token)
}
>>>>>>> feature
`
	a := ClassifyConflict(log)
	if a.Class != ConflictSemantic {
		t.Errorf("delete-vs-modify: got %q, want semantic", a.Class)
	}
	if !strings.Contains(a.Details[0].Reason, "deleted") {
		t.Errorf("reason should mention deleted, got %q", a.Details[0].Reason)
	}
}

func TestClassifyConflict_InterfaceConflict(t *testing.T) {
	log := `UU internal/repo/store.go
--- FILE: internal/repo/store.go ---
<<<<<<< HEAD
type Store interface {
	Get(id string) (*Item, error)
	List() ([]*Item, error)
}
=======
type Store interface {
	Get(id string) (*Item, error)
	Delete(id string) error
}
>>>>>>> feature
`
	a := ClassifyConflict(log)
	if a.Class != ConflictSemantic {
		t.Errorf("interface conflict: got %q, want semantic", a.Class)
	}
}

func TestClassifyConflict_LargeConflictRegion(t *testing.T) {
	// Build a conflict with >5 lines on each side.
	var ours, theirs []string
	for i := 0; i < 10; i++ {
		ours = append(ours, "  line_ours_"+string(rune('a'+i)))
		theirs = append(theirs, "  line_theirs_"+string(rune('a'+i)))
	}
	log := "UU internal/big.go\n--- FILE: internal/big.go ---\n" +
		"<<<<<<< HEAD\n" + strings.Join(ours, "\n") + "\n=======\n" +
		strings.Join(theirs, "\n") + "\n>>>>>>> feature\n"

	a := ClassifyConflict(log)
	if a.Class != ConflictSemantic {
		t.Errorf("large conflict region: got %q, want semantic", a.Class)
	}
	if !strings.Contains(a.Details[0].Reason, "large") {
		t.Errorf("reason should mention large, got %q", a.Details[0].Reason)
	}
}

func TestClassifyConflict_ConfigFileTrivial(t *testing.T) {
	log := "UU config/settings.toml"
	a := ClassifyConflict(log)
	if a.Class != ConflictTrivial {
		t.Errorf("config file: got %q, want trivial", a.Class)
	}
}

func TestClassifyConflict_MixedTrivialSemantic(t *testing.T) {
	// Lock file (trivial) + test file (semantic) → overall semantic.
	log := "UU go.sum\nUU internal/handler_test.go"
	a := ClassifyConflict(log)
	if a.Class != ConflictSemantic {
		t.Errorf("mixed trivial+semantic: got %q, want semantic", a.Class)
	}
	if len(a.Details) != 2 {
		t.Fatalf("expected 2 details, got %d", len(a.Details))
	}
	// Verify per-file classification.
	for _, d := range a.Details {
		if d.File == "go.sum" && d.Class != ConflictTrivial {
			t.Errorf("go.sum should be trivial, got %q", d.Class)
		}
		if d.File == "internal/handler_test.go" && d.Class != ConflictSemantic {
			t.Errorf("handler_test.go should be semantic, got %q", d.Class)
		}
	}
}

func TestClassifyConflict_NoFiles(t *testing.T) {
	a := ClassifyConflict("error: could not apply abc123")
	if a.Class != ConflictTrivial {
		t.Errorf("no files: got %q, want trivial", a.Class)
	}
}

func TestClassifyConflict_MultipleStatusCodes(t *testing.T) {
	// Test AA (both added) and DU (deleted by us) status codes.
	log := "AA new_file.go\nDU removed.go"
	a := ClassifyConflict(log)
	if len(a.Files) != 2 {
		t.Errorf("expected 2 files, got %d", len(a.Files))
	}
}

func TestClassifyConflict_GeneratedDirectory(t *testing.T) {
	log := "UU pkg/generated/types.go"
	a := ClassifyConflict(log)
	if a.Class != ConflictTrivial {
		t.Errorf("generated directory: got %q, want trivial", a.Class)
	}
}

func TestClassifyConflict_YarnLock(t *testing.T) {
	log := "UU yarn.lock"
	a := ClassifyConflict(log)
	if a.Class != ConflictTrivial {
		t.Errorf("yarn.lock: got %q, want trivial", a.Class)
	}
}

func TestClassifyConflict_PythonTestFile(t *testing.T) {
	log := "UU tests/test_auth.py"
	a := ClassifyConflict(log)
	if a.Class != ConflictSemantic {
		t.Errorf("python test file: got %q, want semantic", a.Class)
	}
}

func TestClassifyConflict_TestsDirectory(t *testing.T) {
	log := "UU src/__tests__/helper.ts"
	a := ClassifyConflict(log)
	if a.Class != ConflictSemantic {
		t.Errorf("__tests__ dir: got %q, want semantic", a.Class)
	}
}

// TestHandleConflict_Trivial verifies that a trivial conflict spawns a
// conflict-resolver task with the analysis in the body.
func TestHandleConflict_Trivial(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	repo, _ := st.RepoCreate(ctx, "repo01", "r", "/tmp/r", "main", "", "", "", false)
	st.TaskCreate(ctx, "task01", "", "repo01", "feat", "", "", "", "", 0)
	st.MergeQueueEnqueue(ctx, "mq01", "task01", "repo01", "clankwork/task01", "main", 0)

	item, _ := st.MergeQueueGet(ctx, "mq01")
	p := newTestProcessor(t, st)

	// Lock file only → trivial → should spawn conflict-resolver task.
	if err := p.handleConflict(ctx, item, repo, "UU go.sum"); err != nil {
		t.Fatal(err)
	}

	updatedItem, _ := st.MergeQueueGet(ctx, "mq01")
	if updatedItem.Status != "conflicted" {
		t.Errorf("status = %q, want conflicted", updatedItem.Status)
	}
	if updatedItem.ConflictTaskID == "" {
		t.Error("trivial conflict should spawn resolver task")
	}

	conflictTask, _ := st.TaskGet(ctx, updatedItem.ConflictTaskID)
	if conflictTask.Role != "conflict-resolver" {
		t.Errorf("conflict task role = %q, want conflict-resolver", conflictTask.Role)
	}
	if !strings.Contains(conflictTask.Body, "trivial") {
		t.Errorf("trivial conflict task body should contain analysis class, got: %s", conflictTask.Body)
	}
}

// TestHandleConflict_Semantic verifies that a semantic conflict rejects the
// merge item and re-dispatches the original task.
func TestHandleConflict_Semantic(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	repo, _ := st.RepoCreate(ctx, "repo01", "r", "/tmp/r", "main", "", "", "", false)
	st.TaskCreate(ctx, "task01", "", "repo01", "feat", "", "", "", "", 0)
	st.MergeQueueEnqueue(ctx, "mq01", "task01", "repo01", "clankwork/task01", "main", 0)

	item, _ := st.MergeQueueGet(ctx, "mq01")
	p := newTestProcessor(t, st)

	// Test file conflict → semantic → should reject and re-dispatch.
	if err := p.handleConflict(ctx, item, repo, "UU internal/handler_test.go"); err != nil {
		t.Fatal(err)
	}

	updatedItem, _ := st.MergeQueueGet(ctx, "mq01")
	if updatedItem.Status != "rejected" {
		t.Errorf("status = %q, want rejected (semantic conflict)", updatedItem.Status)
	}

	// Original task should be set back to pending for re-dispatch.
	task, _ := st.TaskGet(ctx, "task01")
	if task.Status != "pending" {
		t.Errorf("task status = %q after semantic conflict, want pending", task.Status)
	}
}

// TestHandleConflict_Semantic_MaxAttempts verifies that semantic conflicts
// also respect max attempts but reject on first semantic detection.
func TestHandleConflict_SemanticIgnoresAttemptCount(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	repo, _ := st.RepoCreate(ctx, "repo01", "r", "/tmp/r", "main", "", "", "", false)
	st.TaskCreate(ctx, "task01", "", "repo01", "feat", "", "", "", "", 0)
	st.MergeQueueEnqueue(ctx, "mq01", "task01", "repo01", "clankwork/task01", "main", 0)

	item, _ := st.MergeQueueGet(ctx, "mq01")
	p := newTestProcessor(t, st)

	// Semantic conflict on first attempt → should still reject immediately.
	if err := p.handleConflict(ctx, item, repo, "UU internal/handler_test.go"); err != nil {
		t.Fatal(err)
	}

	updatedItem, _ := st.MergeQueueGet(ctx, "mq01")
	if updatedItem.Status != "rejected" {
		t.Errorf("status = %q, want rejected on first semantic conflict", updatedItem.Status)
	}
}

func TestExtractConflictFiles(t *testing.T) {
	tests := []struct {
		name  string
		log   string
		want  []string
	}{
		{"UU", "UU file.go", []string{"file.go"}},
		{"AA", "AA new.go", []string{"new.go"}},
		{"DU", "DU deleted.go", []string{"deleted.go"}},
		{"UD", "UD other.go", []string{"other.go"}},
		{"multiple", "UU a.go\nUU b.go\nAA c.go", []string{"a.go", "b.go", "c.go"}},
		{"dedup", "UU a.go\nUU a.go", []string{"a.go"}},
		{"mixed with other lines", "M  clean.go\nUU conflict.go\n?? untracked.go", []string{"conflict.go"}},
		{"empty", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractConflictFiles(tt.log)
			if len(got) != len(tt.want) {
				t.Errorf("extractConflictFiles: got %v, want %v", got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("file[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestCountConflictMarkers(t *testing.T) {
	content := "<<<<<<< HEAD\nours\n=======\ntheirs\n>>>>>>> feat\n<<<<<<< HEAD\nx\n=======\ny\n>>>>>>> feat"
	if got := countConflictMarkers(content); got != 2 {
		t.Errorf("countConflictMarkers = %d, want 2", got)
	}
}

func TestParseConflictBlocks(t *testing.T) {
	content := "before\n<<<<<<< HEAD\nline1\nline2\n=======\nline3\n>>>>>>> feat\nafter"
	blocks := parseConflictBlocks(content)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	if blocks[0].oursLines != 2 {
		t.Errorf("oursLines = %d, want 2", blocks[0].oursLines)
	}
	if blocks[0].theirsLines != 1 {
		t.Errorf("theirsLines = %d, want 1", blocks[0].theirsLines)
	}
}
