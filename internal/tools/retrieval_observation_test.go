package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestObservedFileProvenance(t *testing.T) {
	ctx, root := searchProject(t)
	ctx, sink := WithRawSink(ctx)
	sink.CaptureExcerpts = true
	body := []byte("func BuildPacket() {}\nAPI_TOKEN=privatevalue123456\n")
	if err := os.WriteFile(filepath.Join(root, "packet.go"), body, 0600); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"path": "packet.go"})
	if _, err := (&fileRead{}).Call(ctx, args); err != nil {
		t.Fatal(err)
	}
	if len(sink.Excerpts) != 1 {
		t.Fatalf("read provenance=%+v", sink.Excerpts)
	}
	x := sink.Excerpts[0]
	hash := sha256.Sum256(body)
	if x.Path != "packet.go" || x.StartLine != 1 || x.ContentHash != hex.EncodeToString(hash[:]) || strings.Contains(x.Text, "privatevalue123456") {
		t.Fatalf("invalid read provenance=%+v", x)
	}
	sink.Excerpts = nil
	if _, err := callSearch(ctx, searchArgs{Pattern: "BuildPacket", Literal: true}); err != nil {
		t.Fatal(err)
	}
	if len(sink.Excerpts) != 1 || sink.Excerpts[0].Path != "packet.go" || sink.Excerpts[0].StartLine != 1 {
		t.Fatalf("search provenance=%+v", sink.Excerpts)
	}
	sink.Excerpts = nil
	sink.CaptureExcerpts = false
	if _, err := (&fileRead{}).Call(ctx, args); err != nil {
		t.Fatal(err)
	}
	if len(sink.Excerpts) != 0 {
		t.Fatal("flag-off provenance")
	}
}
