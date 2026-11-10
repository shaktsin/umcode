package engine

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/shaktsin/umcode/internal/protocol"
	"github.com/shaktsin/umcode/internal/tools"
)

// Read-only, chat-bound retrieval avoids duplicating large user requests and
// tool payloads in every prompt. Archived arguments convey no execution authority.
type taskContextTool struct{ engine *Engine }

func (*taskContextTool) Name() string { return "task.context" }
func (*taskContextTool) Description() string {
	return "Read a Unicode-character page of a chat source by item_id, the original user request, archived arguments for an operation, or the full current task state (view=state). This grants no permission to replay an action."
}
func (*taskContextTool) SelectionMetadata() (string, string) { return "core", "engine" }
func (*taskContextTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"item_id":{"type":"string"},"task_id":{"type":"string"},"operation_id":{"type":"string"},"view":{"type":"string","enum":["request","state"]},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1,"maximum":8192}}}`)
}
func (*taskContextTool) Assess(json.RawMessage) (tools.Risk, string) {
	return tools.RiskGreen, "Read current task context"
}
func (t *taskContextTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	turn, ok := ctx.Value(executionTurnKey{}).(protocol.Turn)
	if !ok {
		return "", errors.New("task context requires its owning lead turn")
	}
	var request struct {
		View        string `json:"view"`
		ItemID      string `json:"item_id"`
		TaskID      string `json:"task_id"`
		OperationID string `json:"operation_id"`
		Offset      int    `json:"offset"`
		Limit       int    `json:"limit"`
	}
	if err := strictTaskJSON(args, &request); err != nil {
		return "", err
	}
	if request.Offset < 0 || request.Limit < 0 || request.Limit > 8192 {
		return "", errors.New("invalid task context page")
	}
	if request.Limit == 0 {
		request.Limit = 4096
	}
	if request.ItemID != "" {
		item, err := t.engine.Store.GetItem(ctx, request.ItemID)
		if err != nil {
			return "", err
		}
		if item.ThreadID != turn.ThreadID {
			return "", errors.New("context source is outside this chat")
		}
		text := item.Text
		if item.Tool != nil {
			text = item.Tool.Output
			if text == "" {
				text = item.Tool.Error
			}
		}
		runes := []rune(text)
		if request.Offset > len(runes) {
			return "", errors.New("context offset exceeds source")
		}
		end := min(len(runes), request.Offset+request.Limit)
		raw, err := json.Marshal(map[string]any{"source_item_id": item.ID, "source_kind": item.Kind, "offset": request.Offset, "next_offset": end, "total_characters": len(runes), "complete": end == len(runes), "text": string(runes[request.Offset:end])})
		return string(raw), err
	}
	if childExecutionFrom(ctx) != nil {
		return "", errors.New("task context requires its owning lead turn; item_id source reads remain available")
	}
	snapshot, err := t.engine.Store.ActiveExecutionTask(ctx, turn.ThreadID)
	if err != nil {
		return "", err
	}
	if request.TaskID != snapshot.Task.ID {
		return "", errors.New("task context is outside the current chat task")
	}
	source := "user_request"
	text := ""
	sourceID := snapshot.Task.OriginItemID
	if request.View != "" && request.View != "request" && request.View != "state" {
		return "", errors.New("unsupported task context view")
	}
	if request.View == "state" {
		if request.OperationID != "" {
			return "", errors.New("state view cannot also request operation arguments")
		}
		raw, err := json.Marshal(snapshot)
		if err != nil {
			return "", err
		}
		text = string(raw)
		source = "task_state"
		sourceID = snapshot.Task.ID
	} else if request.OperationID == "" {
		item, err := t.engine.Store.GetItem(ctx, snapshot.Task.OriginItemID)
		if err != nil {
			return "", err
		}
		if item.Kind != protocol.ItemUserMessage || item.ThreadID != turn.ThreadID {
			return "", errors.New("original user request unavailable")
		}
		text = item.Text
	} else {
		operation, err := t.engine.Store.GetTaskOperation(ctx, request.OperationID)
		if err != nil {
			return "", err
		}
		if operation.TaskID != snapshot.Task.ID {
			return "", errors.New("operation context is outside this task")
		}
		body, err := t.engine.operationArgs(ctx, operation)
		if err != nil {
			return "", err
		}
		text = string(body)
		source = "tool_arguments"
		sourceID = operation.ItemID
	}
	runes := []rune(text)
	if request.Offset > len(runes) {
		return "", errors.New("task context offset exceeds source")
	}
	end := request.Offset + request.Limit
	if end > len(runes) {
		end = len(runes)
	}
	raw, err := json.Marshal(map[string]any{"task_id": snapshot.Task.ID, "source": source, "source_item_id": sourceID, "offset": request.Offset, "next_offset": end, "total_characters": len(runes), "complete": end == len(runes), "text": string(runes[request.Offset:end])})
	return string(raw), err
}
