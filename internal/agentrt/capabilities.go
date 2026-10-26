// Package agentrt implements the UMCode agent runtime core.
package agentrt

import (
	"fmt"
	"sort"

	"github.com/shaktsin/umcode/internal/protocol"
)

// ResolveOptions configures capability resolution.
type ResolveOptions struct {
	// ShellReadOnlySupported reports whether a read-only shell can be enforced.
	// When false, a read-only shell grant is downgraded to none.
	ShellReadOnlySupported bool
	// ShellReadOnlyReason says why a read-only shell is unsupported; it
	// replaces the generic reason in the rejection.
	ShellReadOnlyReason string
	// ShellUnavailable, when non-empty, means no confined shell exists for the
	// project at all (confinement none): every shell grant becomes none and the
	// rejection carries this reason.
	ShellUnavailable string
	// NetworkUnavailable, when non-empty, is a network ceiling: the task's
	// project has no network, so no agent may get it whatever the caller and
	// plan allow (authority only narrows down the tree). The rejection carries
	// this reason.
	NetworkUnavailable string
}

// filesRank ranks file access; unknown or empty values rank as none.
func filesRank(f protocol.FileAccess) int {
	switch f {
	case protocol.FilesRead:
		return 1
	case protocol.FilesReadWrite:
		return 2
	}
	return 0
}

func filesKnown(f protocol.FileAccess) bool {
	return f == protocol.FilesNone || f == protocol.FilesRead || f == protocol.FilesReadWrite
}

func filesFromRank(r int) protocol.FileAccess {
	switch r {
	case 1:
		return protocol.FilesRead
	case 2:
		return protocol.FilesReadWrite
	}
	return protocol.FilesNone
}

// shellRank ranks shell access; unknown or empty values rank as none.
func shellRank(s protocol.ShellAccess) int {
	switch s {
	case protocol.ShellReadOnly:
		return 1
	case protocol.ShellReadWrite:
		return 2
	}
	return 0
}

func shellKnown(s protocol.ShellAccess) bool {
	return s == protocol.ShellNone || s == protocol.ShellReadOnly || s == protocol.ShellReadWrite
}

func shellFromRank(r int) protocol.ShellAccess {
	switch r {
	case 1:
		return protocol.ShellReadOnly
	case 2:
		return protocol.ShellReadWrite
	}
	return protocol.ShellNone
}

// Intersect returns the capabilities allowed by both a and b.
func Intersect(a, b protocol.AgentCapabilities) protocol.AgentCapabilities {
	spawn := a.SpawnMax
	if b.SpawnMax < spawn {
		spawn = b.SpawnMax
	}
	return protocol.AgentCapabilities{
		Files:    filesFromRank(min(filesRank(a.Files), filesRank(b.Files))),
		Shell:    shellFromRank(min(shellRank(a.Shell), shellRank(b.Shell))),
		Network:  a.Network && b.Network,
		SpawnMax: spawn,
	}
}

// Within reports whether child is no broader than parent.
func Within(child, parent protocol.AgentCapabilities) bool {
	return filesRank(child.Files) <= filesRank(parent.Files) &&
		shellRank(child.Shell) <= shellRank(parent.Shell) &&
		(!child.Network || parent.Network) &&
		child.SpawnMax <= parent.SpawnMax
}

// Resolve computes granted = parent ∩ entry ∩ requested and reports each
// requested field that was reduced or invalid.
func Resolve(parent, entry, requested protocol.AgentCapabilities, opt ResolveOptions) (protocol.AgentCapabilities, []protocol.CapabilityRejection) {
	granted := Intersect(Intersect(parent, entry), requested)
	var rej []protocol.CapabilityRejection
	add := func(field, reason string) {
		rej = append(rej, protocol.CapabilityRejection{Field: field, Reason: reason})
	}

	// A writable shell can change files that the file tools may not, so it is
	// honoured only together with read-write files: downgrade one rung.
	clamped := false
	if granted.Shell == protocol.ShellReadWrite && granted.Files != protocol.FilesReadWrite {
		granted.Shell = protocol.ShellReadOnly
		clamped = true
	}
	if granted.Shell == protocol.ShellReadOnly && !opt.ShellReadOnlySupported {
		granted.Shell = protocol.ShellNone
	}
	if opt.ShellUnavailable != "" {
		granted.Shell = protocol.ShellNone
	}
	// Any shell can read every file of the checkout, so files "none" allows
	// no shell at all (the mirror of the read-write ruling above).
	filesNone := false
	if granted.Files == protocol.FilesNone && granted.Shell != protocol.ShellNone {
		granted.Shell = protocol.ShellNone
		filesNone = true
	}
	if opt.NetworkUnavailable != "" {
		granted.Network = false
	}

	switch {
	case !filesKnown(requested.Files):
		add("capabilities.files", fmt.Sprintf("unknown value %q", string(requested.Files)))
	case filesRank(requested.Files) > filesRank(granted.Files):
		add("capabilities.files", fmt.Sprintf("requested %q exceeds allowed %q", requested.Files, granted.Files))
	}
	switch {
	case !shellKnown(requested.Shell):
		add("capabilities.shell", fmt.Sprintf("unknown value %q", string(requested.Shell)))
	case shellRank(requested.Shell) > shellRank(granted.Shell):
		reason := fmt.Sprintf("requested %q exceeds allowed %q", requested.Shell, granted.Shell)
		if clamped {
			reason = fmt.Sprintf("read-write shell requires files %q; granted shell %q", protocol.FilesReadWrite, granted.Shell)
		}
		if requested.Shell == protocol.ShellReadOnly && !opt.ShellReadOnlySupported {
			reason = "read-only shell is not supported on this platform"
			if opt.ShellReadOnlyReason != "" {
				reason = "read-only shell is not available: " + opt.ShellReadOnlyReason
			}
		}
		if opt.ShellUnavailable != "" {
			reason = "shell is not available: " + opt.ShellUnavailable
		}
		if filesNone {
			reason = fmt.Sprintf("a shell can read the project's files; files %q allows no shell", protocol.FilesNone)
		}
		add("capabilities.shell", reason)
	}
	if requested.Network && !granted.Network {
		reason := "network access not allowed"
		if opt.NetworkUnavailable != "" {
			reason = "network access is not available: " + opt.NetworkUnavailable
		}
		add("capabilities.network", reason)
	}
	if requested.SpawnMax > granted.SpawnMax {
		add("capabilities.spawnMax", fmt.Sprintf("requested %d exceeds allowed %d", requested.SpawnMax, granted.SpawnMax))
	}
	return granted, rej
}

// RootCapabilities returns the maximal capabilities of a root agent.
func RootCapabilities() protocol.AgentCapabilities {
	return protocol.AgentCapabilities{
		Files:    protocol.FilesReadWrite,
		Shell:    protocol.ShellReadWrite,
		Network:  true,
		SpawnMax: 1000,
	}
}

// AllowedTools returns the sorted tool names permitted by c.
func AllowedTools(c protocol.AgentCapabilities) []string {
	var tools []string
	if filesRank(c.Files) >= 1 {
		tools = append(tools, "file.read", "file.list", "file.search")
	}
	if filesRank(c.Files) >= 2 {
		tools = append(tools, "file.write", "file.edit")
	}
	tools = append(tools, "skill.get_instructions")
	if shellRank(c.Shell) >= 1 {
		tools = append(tools, "shell.run")
	}
	if shellRank(c.Shell) >= 2 {
		tools = append(tools, "verification.run")
	}
	if c.SpawnMax > 0 {
		tools = append(tools, "agent.spawn", "agent.list", "agent.status", "agent.wait", "agent.message", "agent.cancel")
	}
	sort.Strings(tools)
	return tools
}
