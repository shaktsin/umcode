package engine

import "testing"

func TestSameWorkbenchGraphIgnoresFormattingAndKeyOrder(t *testing.T) {
	saved := []byte("{\n  \"version\": 1,\n  \"nodes\": [{\"id\": \"input\", \"config\": {\"vocab\": 259}}],\n  \"edges\": []\n}\n")
	draft := []byte(`{"edges":[],"nodes":[{"config":{"vocab":259},"id":"input"}],"version":1}`)
	if !sameWorkbenchGraph(saved, draft) {
		t.Fatal("semantic match reported unsaved")
	}
	if sameWorkbenchGraph(saved, []byte(`{"version":1,"nodes":[{"id":"input","config":{"vocab":32000}}],"edges":[]}`)) {
		t.Fatal("real edit reported saved")
	}
}
