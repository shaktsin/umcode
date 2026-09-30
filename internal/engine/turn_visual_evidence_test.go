package engine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/tools"
)

func TestComputerActionFeedbackUsesMarkedImageAndPairsCoordinates(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "artifacts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "artifacts", "plain.png"), []byte("plain"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "artifacts", "marked.png"), []byte("marked"), 0o600); err != nil {
		t.Fatal(err)
	}
	report := map[string]any{
		"observation_id": "obs-2",
		"last_action":    map[string]any{"type": "click", "target_description": "Continue button", "element_id": "w0/3", "x": 17, "y": 29, "screenshot_width": 200, "screenshot_height": 100, "observation_id": "obs-1", "result": "dispatched"},
		"artifacts": []map[string]string{
			{"path": "artifacts/plain.png", "mime_type": "image/png", "kind": "screenshot"},
			{"path": "artifacts/marked.png", "mime_type": "image/png", "kind": "screenshot_action"},
		},
	}
	encoded, _ := json.Marshal(report)
	ctx := tools.WithScope(context.Background(), &tools.Scope{Root: root})
	message := visualEvidenceMessage(ctx, string(encoded), "computer.act")
	if message == nil || len(message.Parts) != 2 {
		t.Fatalf("expected screenshot feedback message, got %#v", message)
	}
	imageBytes, err := base64.StdEncoding.DecodeString(message.Parts[1].DataB64)
	if err != nil || string(imageBytes) != "marked" {
		t.Fatalf("model should receive the action-marked image, got %q err=%v", imageBytes, err)
	}
	for _, expected := range []string{"(17, 29)", "obs-1", "200x100", "obs-2", "Continue button", "w0/3", "not proof it succeeded"} {
		if !strings.Contains(message.Parts[0].Text, expected) {
			t.Fatalf("feedback label missing %q: %s", expected, message.Parts[0].Text)
		}
	}
}
