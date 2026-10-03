package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/shaktsin/umcode/internal/compute"
)

type bigOutputCompute struct{}

func (bigOutputCompute) Run(_ context.Context, req compute.Request) (int, error) {
	_, _ = req.Stdout.Write([]byte(strings.Repeat("line of test output\n", 3000) + "VERDICT: FAIL"))
	return 1, nil
}

func TestRawSinkCapturesUnclippedVerificationRun(t *testing.T) {
	tool := &verificationRun{shell: &shellRun{compute: bigOutputCompute{}}}
	base := WithScope(context.Background(), &Scope{ProjectID: "prj_1", ProjectName: "demo", Root: t.TempDir(), AllowNet: true, UseCompute: true})
	ctx, sink := WithRawSink(base)
	out, err := tool.Call(ctx, json.RawMessage(`{"checks":[{"label":"unit","command":"go test"},{"label":"vet","command":"go vet"}],"continue_on_failure":true}`))
	if err != nil {
		t.Fatal(err)
	}
	var raw, visible struct {
		Results []struct {
			Output string `json:"output"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(sink.Text), &raw); err != nil || len(raw.Results) != 2 {
		t.Fatalf("raw sink is not the full result JSON: %v / %.200s", err, sink.Text)
	}
	if err := json.Unmarshal([]byte(out), &visible); err != nil || len(visible.Results) != 2 {
		t.Fatalf("visible output must stay valid JSON: %v", err)
	}
	for i := range raw.Results {
		if len(raw.Results[i].Output) <= 3<<10 || !strings.HasSuffix(raw.Results[i].Output, "VERDICT: FAIL") {
			t.Fatalf("raw result %d is not the full output (%d bytes)", i, len(raw.Results[i].Output))
		}
		if len(visible.Results[i].Output) > 3<<10+100 {
			t.Fatalf("visible result %d grew beyond the clip: %d bytes", i, len(visible.Results[i].Output))
		}
	}
}

func TestSetRawWithoutSinkIsNoop(t *testing.T) {
	SetRaw(context.Background(), "ignored") // must not panic
	ctx, sink := WithRawSink(context.Background())
	SetRaw(ctx, "kept")
	if sink.Text != "kept" {
		t.Fatalf("sink = %q", sink.Text)
	}
}
