// ACP shell-path canonicalization and worktree boundary checks.
package worker

import (
	"path/filepath"
	"strings"
)

func isClankworkCommand(command string) bool {
	command = strings.TrimSpace(command)
	tokens, ok := shellTokens(command)
	if !ok || len(tokens) == 0 {
		return false
	}
	first := filepath.Base(tokens[0])
	if first != "clankwork" {
		return false
	}
	for _, token := range tokens[1:] {
		switch token {
		case ";", "|", "&", "(", ")", "<", ">":
			return false
		}
	}
	return true
}

func isWorktreePermissionRequest(command, workdir string, policy ACPPermissionPolicy) bool {
	command = strings.TrimSpace(command)
	if command == "" || workdir == "" {
		return false
	}
	if referencesSensitivePath(command, policy) {
		return false
	}
	return commandPathsStayInAllowedRoots(command, append([]string{workdir}, policy.AllowPaths...))
}

func isAcceptanceSpecPermissionRequest(command, workdir string, policy ACPPermissionPolicy) bool {
	command = strings.TrimSpace(command)
	if command == "" || workdir == "" {
		return false
	}
	if referencesSensitivePath(command, policy) {
		return false
	}
	if !commandPathsStayInAllowedRoots(command, append([]string{workdir}, policy.AllowPaths...)) {
		return false
	}
	if writesOutsideAcceptanceArtifacts(command) {
		return false
	}
	return true
}

func writesOutsideAcceptanceArtifacts(command string) bool {
	lower := strings.ToLower(command)
	targetsSpecArtifact := strings.Contains(lower, "artifacts/acceptance-spec.json")
	hardDenyMarkers := []string{
		"apply_patch",
		"sed -i",
		"perl -pi",
		"rm ",
		"rm -",
		"mv ",
		"cp ",
		"git add",
		"git commit",
		"gofmt -w",
		"go test",
		"go build",
	}
	for _, marker := range hardDenyMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	artifactWriteMarkers := []string{"edit ", "write ", ">", "tee "}
	for _, marker := range artifactWriteMarkers {
		if strings.Contains(lower, marker) {
			return !targetsSpecArtifact
		}
	}
	return false
}

func referencesSensitivePath(command string, policy ACPPermissionPolicy) bool {
	lower := strings.ToLower(command)
	dangerousShellForms := []string{"$(", "`", "<(", ">("}
	for _, marker := range dangerousShellForms {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	sensitive := []string{
		"~/.ssh",
		"$home/.ssh",
		"${home}/.ssh",
		"/.ssh/",
		"/.aws/",
		"/.config/gcloud/",
		"/.gnupg/",
		"/etc/",
		"id_rsa",
		"id_ed25519",
	}
	sensitive = append(sensitive, policy.DenyPaths...)
	for _, marker := range sensitive {
		if strings.Contains(lower, strings.ToLower(marker)) {
			return true
		}
	}
	return false
}

func commandPathsStayInAllowedRoots(command string, roots []string) bool {
	var absRoots []string
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		absRoot, err := canonicalPath(root)
		if err == nil {
			absRoots = append(absRoots, absRoot)
		}
	}
	if len(absRoots) == 0 {
		return false
	}
	tokens, ok := shellTokens(command)
	if !ok {
		return false
	}
	for i, token := range tokens {
		token = cleanShellPathToken(token)
		if token == "" {
			continue
		}
		if i == 0 && isSystemExecutablePath(token) {
			continue
		}
		if strings.HasPrefix(token, "~/") || strings.HasPrefix(token, "$HOME/") || strings.HasPrefix(token, "${HOME}/") {
			return false
		}
		if !filepath.IsAbs(token) {
			continue
		}
		if isHarmlessOutsidePath(token) {
			continue
		}
		absToken, err := canonicalPath(token)
		if err != nil {
			return false
		}
		if isHarmlessOutsidePath(absToken) {
			continue
		}
		if !pathWithinAny(absToken, absRoots) {
			return false
		}
	}
	return true
}

func isHarmlessOutsidePath(path string) bool {
	switch path {
	case "/dev/null", "/dev/stdout", "/dev/stderr", "/dev/fd/1", "/dev/fd/2":
		return true
	default:
		return false
	}
}

func cleanShellPathToken(token string) string {
	token = strings.TrimSpace(token)
	token = strings.Trim(token, `"'`)
	token = strings.TrimLeft(token, "<>|&;(")
	token = strings.TrimRight(token, ";,")
	token = strings.Trim(token, `"'`)
	if idx := strings.Index(token, "="); idx > 0 {
		token = token[idx+1:]
		token = strings.Trim(token, `"'`)
	}
	return token
}

func shellTokens(command string) ([]string, bool) {
	var tokens []string
	var b strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if b.Len() == 0 {
			return
		}
		tokens = append(tokens, b.String())
		b.Reset()
	}
	for _, r := range command {
		if escaped {
			b.WriteRune(r)
			escaped = false
			continue
		}
		if quote != '\'' && r == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
				continue
			}
			b.WriteRune(r)
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
		case ' ', '\t', '\n', '\r':
			flush()
		case ';', '|', '&', '(', ')':
			flush()
			tokens = append(tokens, string(r))
		case '<', '>':
			flush()
			tokens = append(tokens, string(r))
		default:
			b.WriteRune(r)
		}
	}
	if escaped || quote != 0 {
		return nil, false
	}
	flush()
	return tokens, true
}

func isSystemExecutablePath(token string) bool {
	return strings.HasPrefix(token, "/bin/") ||
		strings.HasPrefix(token, "/usr/bin/") ||
		strings.HasPrefix(token, "/usr/local/bin/") ||
		strings.HasPrefix(token, "/opt/homebrew/bin/") ||
		strings.HasPrefix(token, "/nix/store/") ||
		strings.HasPrefix(token, "/run/current-system/sw/bin/") ||
		isNixProfileBin(token)
}

func isNixProfileBin(token string) bool {
	const prefix = "/etc/profiles/per-user/"
	if !strings.HasPrefix(token, prefix) {
		return false
	}
	rest := token[len(prefix):]
	idx := strings.Index(rest, "/bin/")
	return idx > 0
}

func pathWithin(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, "../"))
}

func canonicalPath(path string) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(absPath); err == nil {
		return resolved, nil
	}
	dir := filepath.Dir(absPath)
	base := filepath.Base(absPath)
	if resolvedDir, err := filepath.EvalSymlinks(dir); err == nil {
		return filepath.Join(resolvedDir, base), nil
	}
	return absPath, nil
}

func pathWithinAny(path string, roots []string) bool {
	for _, root := range roots {
		if pathWithin(path, root) {
			return true
		}
	}
	return false
}
