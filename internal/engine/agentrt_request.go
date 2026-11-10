package engine

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"

	"github.com/shaktsin/umcode/internal/protocol"
)

// An explicit request can carry through short continuation replies. Only user
// messages in this chat count; assistant text, tools and project files do not.
var agentRequestLead = regexp.MustCompile(`(?i)^\s*(?:(?:resume|continue|retry)\s*[-:–—]\s*)+`)
var agentRequestAction = regexp.MustCompile(`(?i)^\s*(?:(?:please|for this task|can you|could you|can we|could we|i|we)\s+)*(?:use|start|spawn|delegate|run|have|ask|want|need|implement|build|review)\b[^\n]{0,160}\b(?:sub)?agents?\b`)
var agentRequestProhibition = regexp.MustCompile(`(?i)\b(?:no|without|never|avoid|skip|do not|don't)\s+(?:\w+\s+){0,4}(?:sub)?agents?\b`)

func explicitAgentRequest(text string) bool {
	text = strings.TrimSpace(text)
	if agentRequestProhibition.MatchString(text) {
		return false
	}
	text = agentRequestLead.ReplaceAllString(text, "")
	return agentRequestAction.MatchString(text) || strings.HasPrefix(strings.ToLower(text), "/superpowers:subagent-driven-development")
}

func isAgentConfirmation(text string) bool {
	switch strings.ToLower(strings.Trim(strings.TrimSpace(text), " .!")) {
	case "yes", "yes please", "ok", "okay", "sure", "go ahead", "proceed", "continue", "resume", "do it":
		return true
	}
	return false
}

func (e *Engine) userRequestedAgents(ctx context.Context, turn protocol.Turn) (bool, error) {
	var submitted string
	var seq int64
	err := e.Store.DB.QueryRowContext(ctx, `SELECT seq, text FROM items WHERE thread_id=? AND turn_id=? AND kind=? ORDER BY seq DESC LIMIT 1`,
		turn.ThreadID, turn.ID, protocol.ItemUserMessage).Scan(&seq, &submitted)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	submitted = strings.TrimSpace(submitted)
	if explicitAgentRequest(submitted) {
		return true, nil
	}
	if !isAgentConfirmation(submitted) {
		return false, nil
	}
	rows, err := e.Store.DB.QueryContext(ctx, `SELECT text FROM items WHERE thread_id=? AND kind=? AND seq<? ORDER BY seq DESC LIMIT 6`,
		turn.ThreadID, protocol.ItemUserMessage, seq)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var previous string
		if err := rows.Scan(&previous); err != nil {
			return false, err
		}
		if explicitAgentRequest(previous) {
			return true, nil
		}
		if !isAgentConfirmation(previous) {
			return false, nil
		}
	}
	return false, rows.Err()
}
