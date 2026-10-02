package projects

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
)

// UMCODE.md is the only instruction file UMCode reads implicitly. AGENTS.md,
// CLAUDE.md and any global agent file are deliberately not scanned.
var instructionNames = []string{"UMCODE.md"}

// maxInstructionBytes caps one instruction file; the rest is dropped with a note.
const maxInstructionBytes = 32 << 10

type instructionCache struct {
	composed string
	sources  []protocol.InstructionSource
	stamps   map[string]time.Time
	nested   string
}

// InstructionsFor composes the project-root UMCODE.md, then nested UMCODE.md
// files from the project root through the hinted path.
func (s *Service) InstructionsFor(ctx context.Context, p protocol.Project, hint string) (string, []protocol.InstructionSource) {
	files := s.instructionFiles(p, hint)
	key := p.ID + "\x00" + hint
	s.mu.Lock()
	c := s.cache[key]
	s.mu.Unlock()
	if c != nil && sameStamps(c.stamps, files) {
		return c.composed, c.sources
	}

	var b strings.Builder
	var sources []protocol.InstructionSource
	stamps := map[string]time.Time{}
	for _, f := range files {
		st, err := os.Stat(f.path)
		if err != nil {
			continue
		}
		stamps[f.path] = st.ModTime()
		data, err := os.ReadFile(f.path)
		if err != nil || len(strings.TrimSpace(string(data))) == 0 {
			continue
		}
		src := protocol.InstructionSource{Scope: f.scope, Path: f.path, Bytes: len(data)}
		if len(data) > maxInstructionBytes {
			data = data[:maxInstructionBytes]
			src.Error = fmt.Sprintf("truncated to %d KB", maxInstructionBytes>>10)
		}
		title := map[string]string{
			"project": fmt.Sprintf("# Project instructions (%s)",
				filepath.Base(f.path)),
			"nested": fmt.Sprintf("# Instructions for %s", filepath.Dir(f.path)),
		}[f.scope]
		b.WriteString("\n" + title + "\n")
		b.Write(data)
		b.WriteString("\n")
		sources = append(sources, src)
	}
	composed := b.String()
	s.mu.Lock()
	s.cache[key] = &instructionCache{composed: composed, sources: sources, stamps: stamps}
	s.mu.Unlock()
	return composed, sources
}

type instructionFile struct {
	scope string
	path  string
}

// instructionFiles lists the candidate UMCODE.md files, project root first.
func (s *Service) instructionFiles(p protocol.Project, hint string) []instructionFile {
	var out []instructionFile
	if p.Root == "" {
		return out
	}
	out = append(out, instructionFilesAt(p.Root, "project")...)
	// Include every nested UMCODE.md root-to-leaf.
	if hint != "" {
		if abs, err := Resolve(p.Root, hint); err == nil {
			dir := abs
			if st, err := os.Stat(abs); err != nil || !st.IsDir() {
				dir = filepath.Dir(abs)
			}
			if within(p.Root, dir) {
				var dirs []string
				for within(p.Root, dir) && dir != p.Root {
					dirs = append(dirs, dir)
					dir = filepath.Dir(dir)
				}
				for i := len(dirs) - 1; i >= 0; i-- {
					out = append(out, instructionFilesAt(dirs[i], "nested")...)
				}
			}
		}
	}
	return out
}

func firstInstructionFile(dir string) string {
	for _, name := range instructionNames {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

func instructionFilesAt(dir, scope string) []instructionFile {
	var out []instructionFile
	for _, name := range instructionNames {
		path := filepath.Join(dir, name)
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			out = append(out, instructionFile{scope: scope, path: path})
		}
	}
	return out
}

func sameStamps(stamps map[string]time.Time, files []instructionFile) bool {
	seen := 0
	for _, f := range files {
		st, err := os.Stat(f.path)
		if err != nil {
			if _, had := stamps[f.path]; had {
				return false
			}
			continue
		}
		seen++
		prev, had := stamps[f.path]
		if !had || !prev.Equal(st.ModTime()) {
			return false
		}
	}
	return seen == len(stamps)
}

func (s *Service) invalidate(projectID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.cache {
		if strings.HasPrefix(k, projectID+"\x00") {
			delete(s.cache, k)
		}
	}
}

// InstructionsPath is the only project instruction file UMCODE writes.
func InstructionsPath(p protocol.Project) string {
	return filepath.Join(p.Root, "UMCODE.md")
}

// Instructions reads (and optionally writes) UMCODE.md and returns the composed prompt.
func (s *Service) Instructions(ctx context.Context, p protocol.Project, content *string) (protocol.ProjectInstructionsResult, error) {
	path := InstructionsPath(p)
	if content != nil {
		if _, err := Resolve(p.Root, path); err != nil {
			return protocol.ProjectInstructionsResult{}, err
		}
		if err := os.WriteFile(path, []byte(*content), 0o644); err != nil {
			return protocol.ProjectInstructionsResult{}, err
		}
		s.invalidate(p.ID)
	}
	own := ""
	if data, err := os.ReadFile(path); err == nil {
		own = string(data)
	}
	st, statErr := os.Lstat(path)
	composed, sources := s.InstructionsFor(ctx, p, "")
	return protocol.ProjectInstructionsResult{
		Composed: composed, Project: own, Path: path, Exists: statErr == nil && !st.IsDir(), Sources: sources,
	}, nil
}

// DraftInstructions scans a bounded set of repository metadata and proposes
// UMCODE.md content. It never writes to the project folder.
func (s *Service) DraftInstructions(p protocol.Project) (protocol.ProjectInstructionDraft, error) {
	if p.Root == "" {
		return protocol.ProjectInstructionDraft{}, fmt.Errorf("project folder is unavailable")
	}
	root, err := filepath.Abs(p.Root)
	if err != nil {
		return protocol.ProjectInstructionDraft{}, err
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return protocol.ProjectInstructionDraft{}, fmt.Errorf("project folder is not available")
	}
	draft := protocol.ProjectInstructionDraft{ScannedFiles: []string{}}
	if _, err := os.Stat(filepath.Join(root, "UMCODE.md")); err == nil {
		draft.ExistingUMCodeFile = true
	}
	ignored := map[string]bool{".git": true, "node_modules": true, "vendor": true, "dist": true, "build": true, ".venv": true, "venv": true, "target": true, "coverage": true}
	var dirs []string
	var walk func(string, int)
	walk = func(dir string, depth int) {
		entries, readErr := os.ReadDir(dir)
		if readErr != nil {
			return
		}
		for _, entry := range entries {
			if len(draft.ScannedFiles) >= 180 {
				return
			}
			rel, _ := filepath.Rel(root, filepath.Join(dir, entry.Name()))
			if entry.IsDir() {
				if depth < 2 && !ignored[entry.Name()] {
					dirs = append(dirs, filepath.ToSlash(rel)+"/")
					walk(filepath.Join(dir, entry.Name()), depth+1)
				}
				continue
			}
			draft.ScannedFiles = append(draft.ScannedFiles, filepath.ToSlash(rel))
		}
	}
	walk(root, 0)
	sort.Strings(draft.ScannedFiles)
	sort.Strings(dirs)

	var b strings.Builder
	fmt.Fprintf(&b, "# UMCode project instructions\n\nProject: %s\n\n", p.Name)
	b.WriteString("## Repository map\n\n")
	if len(dirs) == 0 {
		b.WriteString("- (No subdirectories detected in the bounded scan.)\n")
	} else {
		for _, dir := range dirs {
			fmt.Fprintf(&b, "- `%s`\n", dir)
		}
	}
	b.WriteString("\n## Detected project files\n\n")
	for _, name := range []string{"go.mod", "package.json", "pnpm-workspace.yaml", "Cargo.toml", "pyproject.toml", "requirements.txt", "Makefile", "README.md"} {
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			draft.ScannedFiles = appendUnique(draft.ScannedFiles, name)
			fmt.Fprintf(&b, "- `%s`\n", name)
		}
	}
	b.WriteString("\n## Build and test commands\n\n")
	commands := detectedCommands(root)
	if len(commands) == 0 {
		b.WriteString("- No common build/test command was detected. Inspect the project documentation before choosing commands.\n")
	} else {
		for _, command := range commands {
			fmt.Fprintf(&b, "- `%s`\n", command)
		}
	}
	b.WriteString("\n## Working guidelines\n\n- Read the relevant implementation and tests before editing.\n- Keep changes focused and follow the conventions already used in the repository.\n- Run the most relevant tests or checks and report their results.\n- Do not overwrite user changes or modify generated files unless the task requires it.\n")
	draft.Content = b.String()
	return draft, nil
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func detectedCommands(root string) []string {
	var commands []string
	if data, err := os.ReadFile(filepath.Join(root, "Makefile")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "test:") || strings.HasPrefix(line, "check:") || strings.HasPrefix(line, "lint:") || strings.HasPrefix(line, "build:") {
				commands = append(commands, "make "+strings.TrimSuffix(strings.SplitN(line, ":", 2)[0], "!"))
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
		var pkg struct {
			PackageManager string            `json:"packageManager"`
			Scripts        map[string]string `json:"scripts"`
		}
		if json.Unmarshal(data, &pkg) == nil {
			pm := strings.SplitN(pkg.PackageManager, "@", 2)[0]
			if pm == "" {
				pm = "npm"
			}
			for _, script := range []string{"test", "check", "lint", "build"} {
				if _, ok := pkg.Scripts[script]; ok {
					commands = append(commands, pm+" run "+script)
				}
			}
		}
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
		commands = append(commands, "go test ./...", "go build ./...")
	}
	if _, err := os.Stat(filepath.Join(root, "Cargo.toml")); err == nil {
		commands = append(commands, "cargo test", "cargo build")
	}
	if _, err := os.Stat(filepath.Join(root, "pyproject.toml")); err == nil {
		commands = append(commands, "python -m pytest")
	}
	sort.Strings(commands)
	return commands
}
