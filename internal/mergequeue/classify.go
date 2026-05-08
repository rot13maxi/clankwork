package mergequeue

import (
	"path/filepath"
	"strings"
)

// ConflictClass indicates whether a merge conflict is mechanical or behavioral.
type ConflictClass string

const (
	ConflictTrivial  ConflictClass = "trivial"
	ConflictSemantic ConflictClass = "semantic"
)

// ConflictAnalysis is the result of classifying a set of merge conflicts.
type ConflictAnalysis struct {
	Class   ConflictClass    // overall classification (semantic if ANY file is semantic)
	Files   []string         // conflicting files
	Reason  string           // why this classification was chosen
	Details []ConflictDetail // per-file analysis
}

// ConflictDetail is the classification of a single conflicting file.
type ConflictDetail struct {
	File    string
	Class   ConflictClass
	Reason  string
	Markers int // number of conflict marker blocks in this file
}

// ClassifyConflict analyzes the conflict log produced by a failed rebase and
// classifies the overall conflict as trivial (mechanical) or semantic (behavioral).
//
// The conflict log is expected to contain git rebase output followed by
// `git status --short` output. Conflict markers from the actual file content
// can optionally be appended (separated by a "--- FILE: <path> ---" header per
// file) to allow deeper heuristic analysis.
//
// Rules:
//   - Lock files, generated files → trivial
//   - Test files with conflicts → semantic
//   - Migration/schema files → semantic
//   - Small conflict regions in non-test, non-migration code → trivial
//   - Large or function-body conflicts → semantic
//   - If ANY file is semantic, the whole conflict is semantic
func ClassifyConflict(conflictLog string) ConflictAnalysis {
	files := extractConflictFiles(conflictLog)
	if len(files) == 0 {
		return ConflictAnalysis{
			Class:  ConflictTrivial,
			Reason: "no conflicting files detected",
		}
	}

	fileContents := extractFileConflictBlocks(conflictLog)

	var details []ConflictDetail
	hasSemantic := false

	for _, f := range files {
		d := classifyFile(f, fileContents[f])
		details = append(details, d)
		if d.Class == ConflictSemantic {
			hasSemantic = true
		}
	}

	analysis := ConflictAnalysis{
		Files:   files,
		Details: details,
	}

	if hasSemantic {
		analysis.Class = ConflictSemantic
		// Find the first semantic reason for the summary.
		for _, d := range details {
			if d.Class == ConflictSemantic {
				analysis.Reason = "semantic conflict in " + d.File + ": " + d.Reason
				break
			}
		}
	} else {
		analysis.Class = ConflictTrivial
		analysis.Reason = "all conflicting files are mechanical"
	}

	return analysis
}

// extractConflictFiles parses the conflict log for files in conflict state.
// It looks for `UU <path>` lines from `git status --short` output (unmerged both-modified),
// as well as other unmerged status codes (AA, DU, UD).
func extractConflictFiles(log string) []string {
	seen := make(map[string]bool)
	var files []string

	for _, line := range strings.Split(log, "\n") {
		line = strings.TrimSpace(line)
		if len(line) < 3 {
			continue
		}

		// git status --short unmerged codes: UU, AA, DU, UD, AU, UA
		prefix := line[:2]
		switch prefix {
		case "UU", "AA", "DU", "UD", "AU", "UA":
			path := strings.TrimSpace(line[2:])
			if path != "" && !seen[path] {
				seen[path] = true
				files = append(files, path)
			}
		}
	}

	return files
}

// extractFileConflictBlocks parses optional per-file conflict marker content
// appended to the conflict log. Format:
//
//	--- FILE: path/to/file ---
//	<file content with <<<<<<< ======= >>>>>>> markers>
func extractFileConflictBlocks(log string) map[string]string {
	result := make(map[string]string)
	const prefix = "--- FILE: "
	const suffix = " ---"

	parts := strings.Split(log, prefix)
	for i := 1; i < len(parts); i++ {
		idx := strings.Index(parts[i], suffix)
		if idx < 0 {
			continue
		}
		path := parts[i][:idx]
		content := parts[i][idx+len(suffix):]
		// Trim until next file header or end.
		if nextFile := strings.Index(content, prefix); nextFile >= 0 {
			content = content[:nextFile]
		}
		result[path] = content
	}

	return result
}

// classifyFile determines whether a single file's conflict is trivial or semantic.
func classifyFile(path string, conflictContent string) ConflictDetail {
	ext := strings.ToLower(filepath.Ext(path))
	base := strings.ToLower(filepath.Base(path))

	// Lock files are always trivial.
	if isLockFile(base) {
		return ConflictDetail{
			File:    path,
			Class:   ConflictTrivial,
			Reason:  "lock file (auto-generated)",
			Markers: countConflictMarkers(conflictContent),
		}
	}

	// Generated files are always trivial.
	if isGeneratedFile(path, base, ext) {
		return ConflictDetail{
			File:    path,
			Class:   ConflictTrivial,
			Reason:  "generated file",
			Markers: countConflictMarkers(conflictContent),
		}
	}

	// Test files are semantic — conflicting test assertions indicate
	// contradictory behavioral expectations.
	if isTestFile(path, base) {
		return ConflictDetail{
			File:    path,
			Class:   ConflictSemantic,
			Reason:  "conflicting test file (likely contradictory assertions)",
			Markers: countConflictMarkers(conflictContent),
		}
	}

	// Schema/migration files are semantic.
	if isMigrationFile(path) {
		return ConflictDetail{
			File:    path,
			Class:   ConflictSemantic,
			Reason:  "schema migration conflict",
			Markers: countConflictMarkers(conflictContent),
		}
	}

	// If we have conflict content, analyze the markers.
	markers := countConflictMarkers(conflictContent)
	if conflictContent != "" {
		// Check for function body conflicts (semantic).
		if hasFunctionBodyConflict(conflictContent) {
			return ConflictDetail{
				File:    path,
				Class:   ConflictSemantic,
				Reason:  "conflicting changes within function body",
				Markers: markers,
			}
		}

		// Check for delete-vs-modify conflicts.
		if hasDeleteModifyConflict(conflictContent) {
			return ConflictDetail{
				File:    path,
				Class:   ConflictSemantic,
				Reason:  "one side deleted code the other side modified",
				Markers: markers,
			}
		}

		// Check for interface/type definition conflicts.
		if hasInterfaceConflict(conflictContent, ext) {
			return ConflictDetail{
				File:    path,
				Class:   ConflictSemantic,
				Reason:  "conflicting interface or type definition changes",
				Markers: markers,
			}
		}

		// Small conflicts in regular source files are likely trivial
		// (import ordering, adjacent lines, list additions).
		if markers > 0 && allConflictsSmall(conflictContent) {
			// Check if it looks like import/whitespace/list additions.
			if looksLikeImportOrList(conflictContent) {
				return ConflictDetail{
					File:    path,
					Class:   ConflictTrivial,
					Reason:  "import ordering or list addition conflict",
					Markers: markers,
				}
			}
		}

		// Large conflicts in source files default to semantic.
		if markers > 0 && !allConflictsSmall(conflictContent) {
			return ConflictDetail{
				File:    path,
				Class:   ConflictSemantic,
				Reason:  "large conflicting region in source file",
				Markers: markers,
			}
		}
	}

	// Config files (TOML, YAML, JSON) with no content to analyze — lean trivial
	// since they're often list additions.
	if isConfigFile(ext) {
		return ConflictDetail{
			File:    path,
			Class:   ConflictTrivial,
			Reason:  "config file conflict (likely list addition)",
			Markers: markers,
		}
	}

	// Default: without conflict content to analyze, lean semantic for safety.
	// Source files with unknown conflict patterns should be treated as potentially
	// behavioral.
	return ConflictDetail{
		File:    path,
		Class:   ConflictSemantic,
		Reason:  "source file conflict (unknown pattern, defaulting to semantic)",
		Markers: markers,
	}
}

// isLockFile returns true for dependency lock files.
func isLockFile(base string) bool {
	lockFiles := []string{
		"go.sum",
		"package-lock.json",
		"yarn.lock",
		"pnpm-lock.yaml",
		"bun.lockb",
		"bun.lock",
		"cargo.lock",
		"gemfile.lock",
		"poetry.lock",
		"composer.lock",
		"flake.lock",
	}
	for _, lf := range lockFiles {
		if base == lf {
			return true
		}
	}
	return false
}

// isGeneratedFile returns true for files that are machine-generated.
func isGeneratedFile(path, base, ext string) bool {
	// Protobuf generated files.
	if strings.HasSuffix(path, ".pb.go") || strings.HasSuffix(path, "_pb2.py") || strings.HasSuffix(path, ".pb.ts") {
		return true
	}
	// Mock files.
	if strings.HasPrefix(base, "mock_") || strings.HasSuffix(base, "_mock.go") || strings.HasSuffix(base, "_mock.ts") {
		return true
	}
	// Generated GraphQL, OpenAPI.
	if strings.Contains(path, "generated") || strings.Contains(path, "__generated__") {
		return true
	}
	// Minified bundles.
	if ext == ".min.js" || ext == ".min.css" {
		return true
	}
	return false
}

// isTestFile returns true for test files.
func isTestFile(path, base string) bool {
	if strings.HasSuffix(path, "_test.go") {
		return true
	}
	if strings.HasSuffix(base, ".test.ts") || strings.HasSuffix(base, ".test.js") ||
		strings.HasSuffix(base, ".test.tsx") || strings.HasSuffix(base, ".test.jsx") {
		return true
	}
	if strings.HasSuffix(base, ".spec.ts") || strings.HasSuffix(base, ".spec.js") {
		return true
	}
	if strings.HasSuffix(base, "_test.py") || strings.HasPrefix(base, "test_") {
		return true
	}
	if strings.Contains(path, "__tests__/") || strings.Contains(path, "/tests/") {
		return true
	}
	return false
}

// isMigrationFile returns true for database migration files.
func isMigrationFile(path string) bool {
	lower := strings.ToLower(path)
	return strings.Contains(lower, "migration") ||
		strings.Contains(lower, "migrate") ||
		strings.Contains(lower, "/schema/") ||
		strings.HasSuffix(lower, ".sql")
}

// isConfigFile returns true for common config file extensions.
func isConfigFile(ext string) bool {
	switch ext {
	case ".toml", ".yaml", ".yml", ".json", ".ini", ".cfg":
		return true
	}
	return false
}

// countConflictMarkers counts the number of conflict blocks (<<<<<<< markers).
func countConflictMarkers(content string) int {
	count := 0
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "<<<<<<<") {
			count++
		}
	}
	return count
}

// allConflictsSmall returns true if every conflict block in the content is
// small (each side <= 5 lines).
func allConflictsSmall(content string) bool {
	const maxSmallLines = 5
	blocks := parseConflictBlocks(content)
	if len(blocks) == 0 {
		return true
	}
	for _, b := range blocks {
		if b.oursLines > maxSmallLines || b.theirsLines > maxSmallLines {
			return false
		}
	}
	return true
}

type conflictBlock struct {
	ours       string
	theirs     string
	oursLines  int
	theirsLines int
}

// parseConflictBlocks extracts all conflict marker blocks from file content.
func parseConflictBlocks(content string) []conflictBlock {
	var blocks []conflictBlock
	lines := strings.Split(content, "\n")

	i := 0
	for i < len(lines) {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "<<<<<<<") {
			i++
			continue
		}
		// Found start of conflict block.
		i++
		var oursLines []string
		for i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "=======") {
			oursLines = append(oursLines, lines[i])
			i++
		}
		if i >= len(lines) {
			break
		}
		i++ // skip =======
		var theirsLines []string
		for i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), ">>>>>>>") {
			theirsLines = append(theirsLines, lines[i])
			i++
		}
		if i < len(lines) {
			i++ // skip >>>>>>>
		}
		blocks = append(blocks, conflictBlock{
			ours:        strings.Join(oursLines, "\n"),
			theirs:      strings.Join(theirsLines, "\n"),
			oursLines:   len(oursLines),
			theirsLines: len(theirsLines),
		})
	}
	return blocks
}

// hasFunctionBodyConflict checks if conflict markers are inside a function body.
// Heuristic: if both sides of a conflict contain statements (assignments, returns,
// if/for, method calls), it's likely a function body conflict.
func hasFunctionBodyConflict(content string) bool {
	blocks := parseConflictBlocks(content)
	for _, b := range blocks {
		if looksLikeStatements(b.ours) && looksLikeStatements(b.theirs) {
			return true
		}
	}
	return false
}

// looksLikeStatements checks if content looks like executable code statements
// (not just imports, blank lines, or comments).
func looksLikeStatements(content string) bool {
	statementIndicators := []string{
		"return ", "if ", "for ", "switch ", "case ",
		"err :=", "err =", "= ", ":= ", "var ",
		"fmt.", "log.", "slog.",
		"(ctx", "context.",
	}
	lines := strings.Split(content, "\n")
	statementCount := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") {
			continue
		}
		for _, ind := range statementIndicators {
			if strings.Contains(trimmed, ind) {
				statementCount++
				break
			}
		}
	}
	return statementCount >= 2
}

// hasDeleteModifyConflict checks if one side of a conflict is empty (deleted)
// while the other has content (modified).
func hasDeleteModifyConflict(content string) bool {
	blocks := parseConflictBlocks(content)
	for _, b := range blocks {
		oursEmpty := strings.TrimSpace(b.ours) == ""
		theirsEmpty := strings.TrimSpace(b.theirs) == ""
		if (oursEmpty && !theirsEmpty) || (!oursEmpty && theirsEmpty) {
			return true
		}
	}
	return false
}

// hasInterfaceConflict checks if conflicts involve interface or type definitions.
func hasInterfaceConflict(content string, ext string) bool {
	if ext != ".go" && ext != ".ts" && ext != ".java" {
		return false
	}
	blocks := parseConflictBlocks(content)
	for _, b := range blocks {
		combined := b.ours + "\n" + b.theirs
		if strings.Contains(combined, "interface ") ||
			strings.Contains(combined, "type ") && strings.Contains(combined, "struct ") ||
			strings.Contains(combined, "type ") && strings.Contains(combined, "interface ") {
			return true
		}
	}
	return false
}

// looksLikeImportOrList checks whether all conflict blocks look like import
// statements, list additions, or whitespace changes.
func looksLikeImportOrList(content string) bool {
	blocks := parseConflictBlocks(content)
	if len(blocks) == 0 {
		return false
	}
	for _, b := range blocks {
		if !looksLikeImportBlock(b.ours) && !looksLikeImportBlock(b.theirs) &&
			!looksLikeListEntry(b.ours) && !looksLikeListEntry(b.theirs) {
			return false
		}
	}
	return true
}

// looksLikeImportBlock checks if content looks like import statements.
func looksLikeImportBlock(content string) bool {
	lines := nonBlankLines(content)
	if len(lines) == 0 {
		return true
	}
	importCount := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "import ") || strings.HasPrefix(trimmed, "\"") ||
			strings.HasPrefix(trimmed, "from ") || strings.HasPrefix(trimmed, "require(") ||
			trimmed == ")" || trimmed == "(" {
			importCount++
		}
	}
	return importCount == len(lines)
}

// looksLikeListEntry checks if content looks like entries being added to a list
// (e.g., TOML arrays, Go slices, route registrations).
func looksLikeListEntry(content string) bool {
	lines := nonBlankLines(content)
	if len(lines) == 0 {
		return true
	}
	listIndicators := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Lines that start with -, {, ", or are short comma-terminated values.
		if strings.HasPrefix(trimmed, "-") || strings.HasPrefix(trimmed, "{") ||
			strings.HasPrefix(trimmed, "\"") || strings.HasSuffix(trimmed, ",") ||
			strings.HasPrefix(trimmed, "//") {
			listIndicators++
		}
	}
	return listIndicators >= len(lines)/2
}

func nonBlankLines(s string) []string {
	var result []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			result = append(result, line)
		}
	}
	return result
}
