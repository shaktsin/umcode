package main

import "testing"

func TestFindEngines(t *testing.T) {
	out := `  101 /Applications/UMCode.app/Contents/MacOS/umcode-app
  202 /Applications/UMCode.app/Contents/Resources/umcode engine
  303 /Users/me/go/bin/umcode engine --verbose
  404 /usr/bin/vim umcode.go
  505 /Users/me/projects/umcode/umcode chat hello
  606 /bin/zsh -c umcode engine`
	got := findEngines(parsePS(out), 303)
	if len(got) != 1 || got[0] != 202 {
		t.Fatalf("got %v", got)
	}
	if isEngineCommand("umcode") || isEngineCommand("") {
		t.Fatal("false positive")
	}
}
