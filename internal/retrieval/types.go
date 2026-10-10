// Package retrieval selects bounded, provenance-bearing context without I/O.
package retrieval

import "time"

const (
	Deadline          = 100 * time.Millisecond
	MaxInputBytes     = 8192
	MaxTerms          = 16
	MaxTermBytes      = 64
	MaxGraphDepth     = 2
	MaxGraphNodes     = 128
	MaxCandidates     = 64
	MaxEntries        = 12
	MaxTokens         = 6144
	MaxBodyBytes      = 4096
	MaxCandidateBytes = 512 * 1024
	MaxFiles          = 4
	MaxFileBytes      = 256 * 1024
	MaxReadBytes      = 1024 * 1024
	MaxLines          = 40
)

type Scope struct{ ThreadID, WorkID, ProjectID, TurnID string }
type Query struct {
	Terms          []string
	FTS            string
	Paths, Symbols []string
}
type Candidate struct {
	ID, Kind, ThreadID, WorkID, ProjectID, Body, SourceRevision, ContentHash, Path, WorkspaceRootHash string
	StartLine, EndLine, Distance                                                                      int
	LexicalScore                                                                                      float64
	Historical                                                                                        bool
}
type Entry = Candidate
type Report struct {
	Candidates int            `json:"candidates"`
	Selected   int            `json:"selected"`
	Tokens     int            `json:"tokens"`
	Drops      map[string]int `json:"drops,omitempty"`
	Fallback   string         `json:"fallback,omitempty"`
}
