package projects

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shaktsin/umcode/internal/pathutil"
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

// InstructionInventory checks canonical scopes and inventories only literal
// UMCODE.md files. It reads metadata, never instruction contents, and returns no
// partial inventory on failure. Directory symlinks are not traversed.
func InstructionInventory(root string, scopePaths []string) ([]string, error) {
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("instruction inventory project invalid")
	}
	resolvedRoot, st, err := guardedInstructionPath(root, false)
	if err != nil || !st.IsDir() {
		return nil, fmt.Errorf("instruction inventory project unavailable")
	}
	root = resolvedRoot
	for _, scope := range scopePaths {
		if !instructionRelativePath(scope) {
			return nil, fmt.Errorf("instruction inventory scope invalid")
		}
		_, st, err := checkedInstructionPath(root, scope, false)
		if err != nil {
			return nil, err
		}
		if err != nil || !(st.IsDir() || st.Mode().IsRegular()) {
			return nil, fmt.Errorf("instruction inventory scope unavailable")
		}
	}
	var existing []string
	err = filepath.WalkDir(root, func(abs string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("instruction inventory unavailable")
		}
		if foreignInstructionName(entry.Name()) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() != "UMCODE.md" {
			return nil
		}
		if _, err := containedInstructionFile(root, abs); err != nil {
			return err
		}
		existing = append(existing, pathutil.Rel(root, abs))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(existing)
	return existing, nil
}

func foreignInstructionName(name string) bool {
	return name == "AGENTS.md" || name == "CLAUDE.md" || name == "AGENT.md"
}

func instructionRelativePath(p string) bool {
	if p == "" || strings.TrimSpace(p) != p || path.IsAbs(p) || path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../") || strings.HasPrefix(p, "~") || strings.ContainsAny(p, "\\\x00") || len(p) > 1 && p[1] == ':' {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if foreignInstructionName(part) {
			return false
		}
	}
	return true
}

// forbiddenInstructionPath checks the raw spelling before cleaning can erase a
// component or a symlink lookup can follow it. It performs no filesystem I/O.
func forbiddenInstructionPath(candidate string) bool {
	for _, component := range strings.FieldsFunc(candidate, func(r rune) bool { return r == '/' || r == '\\' }) {
		if foreignInstructionName(component) {
			return true
		}
	}
	return false
}

// guardedInstructionPath resolves one component at a time, rejecting every raw
// symlink destination before touching it. EvalSymlinks/Stat cannot provide that
// guarantee: both can follow a forbidden intermediate name before returning.
func guardedInstructionPath(candidate string, allowMissing bool) (string, os.FileInfo, error) {
	if !filepath.IsAbs(candidate) || forbiddenInstructionPath(candidate) {
		return "", nil, fmt.Errorf("instruction path forbidden")
	}
	// Initial roots and hints follow Resolve's lexical cleaning. Link
	// destinations below remain raw so every forbidden hop is checked first.
	candidate = filepath.Clean(candidate)
	volume := filepath.VolumeName(candidate)
	current := volume + string(filepath.Separator)
	pending := strings.Split(filepath.ToSlash(strings.TrimPrefix(candidate, volume)), "/")
	var info os.FileInfo
	links := 0
	for len(pending) > 0 {
		part := pending[0]
		pending = pending[1:]
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			current = filepath.Dir(current)
			info = nil
			continue
		}
		next := filepath.Join(current, part)
		st, err := os.Lstat(next)
		if err != nil {
			if allowMissing && os.IsNotExist(err) {
				for _, remaining := range pending {
					if remaining == ".." {
						return "", nil, fmt.Errorf("instruction path unavailable")
					}
				}
				return filepath.Join(append([]string{next}, pending...)...), nil, nil
			}
			return "", nil, fmt.Errorf("instruction path unavailable")
		}
		if st.Mode()&os.ModeSymlink != 0 {
			links++
			if links > 40 {
				return "", nil, fmt.Errorf("instruction path symlink limit")
			}
			destination, err := os.Readlink(next)
			if err != nil {
				return "", nil, fmt.Errorf("instruction path unavailable")
			}
			if forbiddenInstructionPath(destination) {
				return "", nil, fmt.Errorf("instruction path forbidden")
			}
			if filepath.IsAbs(destination) {
				volume = filepath.VolumeName(destination)
				current = volume + string(filepath.Separator)
				destination = strings.TrimPrefix(destination, volume)
			}
			pending = append(strings.Split(filepath.ToSlash(destination), "/"), pending...)
			info = nil
			continue
		}
		if len(pending) > 0 && !st.IsDir() {
			return "", nil, fmt.Errorf("instruction path is not a directory")
		}
		current, info = next, st
	}
	if info == nil {
		var err error
		info, err = os.Lstat(current)
		if err != nil {
			return "", nil, fmt.Errorf("instruction path unavailable")
		}
	}
	return current, info, nil
}

func checkedInstructionPath(root, candidate string, allowMissing bool) (string, os.FileInfo, error) {
	candidate = strings.TrimSpace(candidate)
	if forbiddenInstructionPath(candidate) {
		return "", nil, fmt.Errorf("instruction path forbidden")
	}
	// Resolve lexically cleans its input before following links. Guard that same
	// spelling, including for absolute hints, after checking raw forbidden names.
	candidate = filepath.Clean(candidate)
	abs := candidate
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, abs)
	}
	_, info, err := guardedInstructionPath(abs, allowMissing)
	if err != nil {
		return "", nil, err
	}
	// Apply the existing containment policy only after every destination has
	// passed the guard. Preserve its lexical spelling for relative hints so a
	// safe directory alias retains its original instruction ancestors.
	contained, err := Resolve(root, candidate)
	if err != nil {
		return "", nil, fmt.Errorf("instruction path outside project")
	}
	return contained, info, nil
}

// containedInstructionFile is shared by inventory and prompt composition, so
// neither can treat an escaping link or a non-regular entry as guidance.
func containedInstructionFile(root, candidate string) (string, error) {
	_, st, err := checkedInstructionPath(root, candidate, false)
	if err != nil {
		return "", err
	}
	if err != nil || !st.Mode().IsRegular() {
		return "", fmt.Errorf("instruction file is not regular")
	}
	return candidate, nil
}

// instructionFiles lists the candidate UMCODE.md files, project root first.
func (s *Service) instructionFiles(p protocol.Project, hint string) []instructionFile {
	var out []instructionFile
	if p.Root == "" {
		return out
	}
	root, st, err := guardedInstructionPath(p.Root, false)
	if err != nil || !st.IsDir() {
		return out
	}
	out = append(out, instructionFilesAt(root, p.Root, "project")...)
	// Include every nested UMCODE.md root-to-leaf.
	if hint != "" {
		if abs, st, err := checkedInstructionPath(root, hint, true); err == nil {
			dir := abs
			if st == nil || !st.IsDir() {
				dir = filepath.Dir(abs)
			}
			if within(root, dir) {
				var dirs []string
				for within(root, dir) && dir != root {
					dirs = append(dirs, dir)
					dir = filepath.Dir(dir)
				}
				for i := len(dirs) - 1; i >= 0; i-- {
					rel, err := filepath.Rel(root, dirs[i])
					if err == nil {
						out = append(out, instructionFilesAt(root, filepath.Join(p.Root, rel), "nested")...)
					}
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

func instructionFilesAt(root, dir, scope string) []instructionFile {
	var out []instructionFile
	for _, name := range instructionNames {
		path := filepath.Join(dir, name)
		if _, err := containedInstructionFile(root, path); err == nil {
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
