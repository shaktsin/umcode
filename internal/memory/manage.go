package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/store"
	"strings"
)

func validCorrectionText(s string) bool {
	return len(s) > 0 && len(s) <= 2048 && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\r\n\x00") && !strings.HasPrefix(s, "-") && !strings.HasPrefix(s, "#")
}

// removeGeneratedMemory removes only an exact single-line item inside UMCode's
// generated section. Any ambiguous structure fails closed.
func removeGeneratedMemory(current []byte, text string) ([]byte, bool, bool) {
	section, ok := parseMemorySection(current)
	if !ok {
		return nil, false, false
	}
	if section.marker < 0 {
		return append([]byte(nil), current...), false, true
	}
	found := false
	for i := section.marker + 1; i < section.end; i++ {
		line := section.lines[i]
		expected := text
		if !strings.HasPrefix(expected, "- ") {
			expected = "- " + expected
		}
		if line.text != expected {
			continue
		}
		if found {
			return nil, false, false
		}
		found = true
		current = append(append([]byte(nil), current[:line.start]...), current[line.next:]...)
		break
	}
	return current, found, true
}

// Retire marks generated knowledge inactive and records a tombstone. User text
// that no longer matches the generated bullet is preserved and reported.
func (s *Service) Retire(ctx context.Context, p protocol.MemoryChangeParams) (protocol.MemoryChangeResult, error) {
	m, err := s.st.GetProjectMemory(ctx, p.MemoryID)
	if err != nil {
		return protocol.MemoryChangeResult{}, err
	}
	if m.ProjectID != p.ProjectID {
		return protocol.MemoryChangeResult{}, store.ErrNotFound
	}
	lock := projectLock(m.ProjectID)
	lock.Lock()
	defer lock.Unlock()
	if p.ExpectedTextHash != m.TextHash {
		return protocol.MemoryChangeResult{}, store.ErrMemoryStale
	}
	project, err := s.projects.Get(ctx, m.ProjectID)
	if err != nil {
		return protocol.MemoryChangeResult{}, err
	}
	target, err := openTarget(project.Root, m.TargetPath)
	if err != nil {
		return protocol.MemoryChangeResult{}, err
	}
	defer target.close()
	if p.ExpectedTargetHash != "" && memoryHash(target.before) != p.ExpectedTargetHash {
		return protocol.MemoryChangeResult{}, store.ErrMemoryStale
	}
	if err = target.check(memoryHash(target.before)); err != nil {
		return protocol.MemoryChangeResult{}, err
	}
	after, removed, valid := removeGeneratedMemory(target.before, m.Text)
	if !valid {
		return protocol.MemoryChangeResult{}, store.ErrMemoryConflict
	}
	if removed && bytes.Equal(after, target.before) {
		return protocol.MemoryChangeResult{}, errors.New("memory file unchanged")
	}
	if removed {
		if err = target.replace(after, s.step); err != nil {
			return protocol.MemoryChangeResult{}, err
		}
	}
	if err = s.st.RetireProjectMemory(ctx, m.ID, m.TextHash); err != nil {
		return protocol.MemoryChangeResult{}, err
	}
	if err = s.st.RecordMemoryRetirement(ctx, m.ProjectID, m.SemanticKey, m.SourceRevision, m.TextHash); err != nil {
		return protocol.MemoryChangeResult{}, err
	}
	entry, _ := s.st.GetProjectMemory(ctx, m.ID)
	out := protocol.MemoryChangeResult{Entry: managedMemoryReview(entry), InstructionRemains: !removed}
	if out.InstructionRemains {
		out.Warning = "The instruction was edited or is user-owned and remains in UMCODE.md."
	}
	return out, nil
}

// Correct creates an explicitly user-owned successor after retiring the exact
// generated line. It does not claim the correction was independently verified.
func (s *Service) Correct(ctx context.Context, p protocol.MemoryChangeParams) (protocol.MemoryChangeResult, error) {
	if !validCorrectionText(p.Text) {
		return protocol.MemoryChangeResult{}, protocol.Errorf(protocol.CodeInvalidParams, "correction must be one non-empty line up to 2048 bytes")
	}
	old, err := s.st.GetProjectMemory(ctx, p.MemoryID)
	if err != nil {
		return protocol.MemoryChangeResult{}, err
	}
	if old.ProjectID != p.ProjectID {
		return protocol.MemoryChangeResult{}, store.ErrNotFound
	}
	lock := projectLock(old.ProjectID)
	lock.Lock()
	defer lock.Unlock()
	if p.ExpectedTextHash != old.TextHash {
		return protocol.MemoryChangeResult{}, store.ErrMemoryStale
	}
	project, err := s.projects.Get(ctx, old.ProjectID)
	if err != nil {
		return protocol.MemoryChangeResult{}, err
	}
	target, err := openTarget(project.Root, old.TargetPath)
	if err != nil {
		return protocol.MemoryChangeResult{}, err
	}
	defer target.close()
	if p.ExpectedTargetHash != "" && memoryHash(target.before) != p.ExpectedTargetHash {
		return protocol.MemoryChangeResult{}, store.ErrMemoryStale
	}
	after, removed, ok := removeGeneratedMemory(target.before, old.Text)
	if !ok {
		return protocol.MemoryChangeResult{}, store.ErrMemoryConflict
	}
	remains := !removed
	if removed {
		section, valid := parseMemorySection(after)
		if !valid {
			return protocol.MemoryChangeResult{}, store.ErrMemoryConflict
		}
		eol := memoryEOL(after)
		line := []byte("- " + p.Text + eol)
		insert := section.insert
		after = append(append(append([]byte(nil), after[:insert]...), line...), after[insert:]...)
		if err = target.replace(after, s.step); err != nil {
			return protocol.MemoryChangeResult{}, err
		}
	}
	entry, err := s.st.CorrectProjectMemory(ctx, old, p.Text, remains)
	if err != nil {
		return protocol.MemoryChangeResult{}, err
	}
	_ = s.st.RecordMemoryRetirement(ctx, old.ProjectID, old.SemanticKey, old.SourceRevision, old.TextHash)
	out := protocol.MemoryChangeResult{Entry: managedMemoryReview(entry), InstructionRemains: remains}
	if remains {
		out.Warning = "The original instruction was edited or user-owned; it remains in UMCODE.md."
	}
	return out, nil
}

func managedMemoryReview(m protocol.ProjectMemory) protocol.MemoryReviewEntry {
	var ids []string
	_ = json.Unmarshal([]byte(m.EvidenceJSON), &ids)
	if ids == nil {
		ids = []string{}
	}
	return protocol.MemoryReviewEntry{ID: m.ID, ProjectID: m.ProjectID, SemanticKey: m.SemanticKey, Category: m.Category, TargetPath: m.TargetPath, Text: m.Text, TextHash: m.TextHash, TargetHash: m.FileHashAfter, Status: m.Status, SourceRevision: m.SourceRevision, EvidenceIDs: ids, UserOwned: m.UserOwned, CreatedAt: m.CreatedAt, PromotedAt: m.PromotedAt, Revision: 1}
}

func (s *Service) List(ctx context.Context, p protocol.MemoryListParams) (protocol.MemoryListResult, error) {
	project, err := s.projects.Get(ctx, p.ProjectID)
	if err != nil {
		return protocol.MemoryListResult{}, err
	}
	result, err := s.st.ListMemoryReview(ctx, p)
	if err != nil {
		return result, err
	}
	hashes := map[string]string{}
	for i := range result.Entries {
		target := result.Entries[i].TargetPath
		hash, ok := hashes[target]
		if !ok {
			opened, openErr := openTarget(project.Root, target)
			if openErr != nil {
				return protocol.MemoryListResult{}, openErr
			}
			hash = memoryHash(opened.before)
			opened.close()
			hashes[target] = hash
		}
		result.Entries[i].TargetHash = hash
	}
	return result, nil
}
