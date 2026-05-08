package scheduler

import (
	"testing"

	"github.com/rot13maxi/clankwork/internal/model"
)

func TestTriageTask_UserOverride(t *testing.T) {
	task := &model.Task{Template: "custom-workflow"}
	if got := TriageTask(task); got != "custom-workflow" {
		t.Errorf("TriageTask() = %q, want %q", got, "custom-workflow")
	}
}

func TestTriageTask_BugfixPrefix(t *testing.T) {
	for _, title := range []string{"fix login crash", "Fix the bug", "bug in parser", "Bug: NPE"} {
		task := &model.Task{Title: title}
		if got := TriageTask(task); got != "bugfix" {
			t.Errorf("TriageTask(title=%q) = %q, want bugfix", title, got)
		}
	}
}

func TestTriageTask_RefactorPrefix(t *testing.T) {
	task := &model.Task{Title: "Refactor the auth module"}
	if got := TriageTask(task); got != "refactor" {
		t.Errorf("TriageTask() = %q, want refactor", got)
	}
}

func TestTriageTask_Simple(t *testing.T) {
	task := &model.Task{Title: "Add a comment", Body: "short body"}
	if got := TriageTask(task); got != "simple" {
		t.Errorf("TriageTask() = %q, want simple", got)
	}
}

func TestTriageTask_SimpleWithAcceptanceCriteria(t *testing.T) {
	task := &model.Task{Title: "Add logging", Body: "Must log all errors"}
	if got := TriageTask(task); got != "feature" {
		t.Errorf("TriageTask() = %q, want feature (body has acceptance keyword)", got)
	}
}

func TestTriageTask_Feature(t *testing.T) {
	task := &model.Task{
		Title: "Implement user authentication",
		Body:  "We need a full authentication system with login, signup, password reset, and session management. The system should support OAuth providers and MFA.",
	}
	if got := TriageTask(task); got != "feature" {
		t.Errorf("TriageTask() = %q, want feature", got)
	}
}
