package store

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shaktsin/umcode/internal/protocol"
)

// Raw model arguments have not passed through json.Marshal. Saving their tool
// item must not make the exact same file payload fail the integrity check.
func TestTaskLargeArgumentsSerialization(t *testing.T) {
	st, th := workFixture(t)
	ctx := t.Context()
	turn := protocol.Turn{ID: NewID("trn"), ThreadID: th.ID, Status: "running", StartedAt: time.Now()}
	if err := st.CreateTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	user := protocol.Item{ID: NewID("itm"), ThreadID: th.ID, TurnID: turn.ID, Seq: 1, Kind: protocol.ItemUserMessage, Text: "Write the plan", CreatedAt: time.Now()}
	if err := st.SaveItem(ctx, user); err != nil {
		t.Fatal(err)
	}
	task, err := st.CreateExecutionTask(ctx, protocol.ExecutionTask{ThreadID: th.ID, OriginItemID: user.ID, Objective: user.Text})
	if err != nil {
		t.Fatal(err)
	}
	seq := int64(1)
	for _, tc := range []struct{ name, suffix string }{
		{"html", "\n> plan instructions\n<div>A & B</div>"},
		{"unicode_separators", "\n计划 🐈 café\u2028line\u2029paragraph"},
		{"literal_escapes", `\nLiteral \u003e and \u0026, then actual > & <`},
		{"control_characters", "\n\tquoted \"value\", backslash \\ and carriage\rreturn"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content := strings.Repeat("Plan section\n", 2000) + tc.suffix
			var buf bytes.Buffer
			encoder := json.NewEncoder(&buf)
			encoder.SetEscapeHTML(false)
			if err := encoder.Encode(map[string]string{"path": "plan.md", "content": content}); err != nil {
				t.Fatal(err)
			}
			raw := bytes.TrimSpace(buf.Bytes())
			if tc.name == "unicode_separators" {
				raw = bytes.ReplaceAll(raw, []byte(`\u2028`), []byte("\u2028"))
				raw = bytes.ReplaceAll(raw, []byte(`\u2029`), []byte("\u2029"))
			}
			if len(raw) <= 16384 || !json.Valid(raw) {
				t.Fatal("fixture must be a valid large raw provider call")
			}
			seq++
			item := protocol.Item{ID: NewID("itm"), ThreadID: th.ID, TurnID: turn.ID, Seq: seq, Kind: protocol.ItemToolCall, Status: protocol.ItemInProgress, Tool: &protocol.ToolCallData{Name: "file.write", Args: raw}, CreatedAt: time.Now()}
			if err := st.SaveItem(ctx, item); err != nil {
				t.Fatal(err)
			}
			op, err := st.CreateTaskOperation(ctx, protocol.TaskOperation{TaskID: task.ID, TurnID: turn.ID, ItemID: item.ID, Tool: "file.write", Args: raw})
			if err != nil {
				t.Fatalf("storage serialization rejected unchanged arguments: %v", err)
			}
			if op.ArgsRef != item.ID || op.ArgsSHA256 == "" || len(op.Args) != 0 {
				t.Fatalf("large arguments not retained by reference: %+v", op)
			}
			saved, err := st.GetItem(ctx, item.ID)
			if err != nil {
				t.Fatal(err)
			}
			var decoded map[string]string
			if err := json.Unmarshal(saved.Tool.Args, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded["content"] != content {
				t.Fatal("normalization changed file contents")
			}
			for _, change := range []struct{ name, path, body, tool string }{
				{"changed_content", "plan.md", content + " changed", "file.write"},
				{"changed_path", "other.md", content, "file.write"},
				{"literal_escape_is_not_character", "plan.md", content + `\u003e`, "file.write"},
				{"changed_tool", "plan.md", content, "file.edit"},
			} {
				t.Run(change.name, func(t *testing.T) {
					changed, _ := json.Marshal(map[string]string{"path": change.path, "content": change.body})
					if _, err := st.CreateTaskOperation(ctx, protocol.TaskOperation{TaskID: task.ID, TurnID: turn.ID, ItemID: item.ID, Tool: change.tool, Args: changed}); err == nil {
						t.Fatal("genuinely changed arguments accepted")
					}
				})
			}
		})
	}
}
