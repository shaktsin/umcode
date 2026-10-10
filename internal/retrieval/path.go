package retrieval

import (
	"path/filepath"
	"strings"
)

// AllowedExcerptPath is stricter than explicit user-directed file tools.
func AllowedExcerptPath(path string) bool {
	if path == "" || path == "." || filepath.IsAbs(path) || filepath.ToSlash(filepath.Clean(path)) != path || strings.Contains(path, "\\") {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		p := strings.ToLower(part)
		switch p {
		case "..", ".git", ".hg", ".svn", "node_modules", "__pycache__", ".venv", ".aws", ".ssh", "agent.md", "agents.md", "claude.md", "umcode.md", "credentials.json", "id_rsa", "id_ed25519":
			return false
		}
		if p == ".env" || strings.HasPrefix(p, ".env.") || strings.HasSuffix(p, ".pem") || strings.HasSuffix(p, ".key") {
			return false
		}
	}
	return true
}
