package scheduler

import (
	"strings"

	"github.com/rot13maxi/clankwork/internal/model"
)

// TriageTask returns the workflow template name for a task.
// If the task already has a template set (user override), it is returned unchanged.
// Otherwise, a template is auto-classified based on heuristics.
func TriageTask(task *model.Task) string {
	if task.Template != "" {
		return task.Template
	}

	titleLower := strings.ToLower(task.Title)

	if strings.HasPrefix(titleLower, "fix") || strings.HasPrefix(titleLower, "bug") {
		return "bugfix"
	}
	if strings.HasPrefix(titleLower, "refactor") {
		return "refactor"
	}

	body := task.Body
	if len(body) < 100 && !hasAcceptanceCriteria(body) {
		return "simple"
	}

	return "feature"
}

func hasAcceptanceCriteria(body string) bool {
	lower := strings.ToLower(body)
	keywords := []string{"acceptance criteria", "requirements", "must", "should", "expected behavior"}
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}
