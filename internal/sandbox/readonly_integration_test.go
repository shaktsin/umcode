package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runIn(t *testing.T, sb Sandbox, p Policy, dir, script string, env ...string) error {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	if err := sb.Wrap(cmd, p); err != nil {
		t.Fatal(err)
	}
	return cmd.Run()
}

func listing(t *testing.T, dir string) string {
	t.Helper()
	var names []string
	filepath.Walk(dir, func(p string, _ os.FileInfo, _ error) error {
		names = append(names, p)
		return nil
	})
	return strings.Join(names, "\n")
}

func TestReadOnlyChildPolicyBlocksEveryWrite(t *testing.T) {
	sb := Detect()
	if sb == nil {
		t.Skip("no OS sandbox is usable here")
	}
	root := t.TempDir()
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := ReadOnlyChildPolicy(root, false, tmp)
	before := listing(t, root)
	env := []string{"TMP=" + realPath(tmp)}
	if err := runIn(t, sb, p, root, "cat a.txt", env...); err != nil {
		t.Fatalf("read failed: %v", err)
	}
	for _, c := range []string{"echo x > b.txt", "touch c", "rm a.txt", "mkdir d", "ln -s a.txt e"} {
		if err := runIn(t, sb, p, root, c, env...); err == nil {
			t.Errorf("%q succeeded under a read-only root", c)
		}
	}
	if err := runIn(t, sb, p, root, `echo x > "$TMP/ok"`, env...); err != nil {
		t.Errorf("temporary write failed: %v", err)
	}
	if after := listing(t, root); after != before {
		t.Errorf("root changed:\nbefore %s\nafter  %s", before, after)
	}
	if err := SelfCheckReadOnly(sb, t.TempDir()); err != nil {
		t.Errorf("SelfCheckReadOnly: %v", err)
	}
}

func TestWriteCapableAgentCannotTouchDotGit(t *testing.T) {
	sb := Detect()
	if sb == nil {
		t.Skip("no OS sandbox is usable here")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	tmp := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "a@b"}, {"config", "user.name", "a"}, {"commit", "-q", "--allow-empty", "-m", "x"}} {
		c := exec.Command("git", args...)
		c.Dir = root
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	p := AgentWritePolicy(root, false, tmp)
	before := listing(t, filepath.Join(root, ".git"))
	cfg, _ := os.ReadFile(filepath.Join(root, ".git", "config"))
	for _, c := range []string{"git commit -q --allow-empty -m y", "echo x >> .git/config", "rm -rf .git/refs"} {
		if err := runIn(t, sb, p, root, c); err == nil {
			t.Errorf("%q succeeded against .git", c)
		}
	}
	for _, c := range []string{"git log", "git diff", "echo ok > f.txt"} {
		if err := runIn(t, sb, p, root, c); err != nil {
			t.Errorf("%q should work: %v", c, err)
		}
	}
	if after := listing(t, filepath.Join(root, ".git")); after != before {
		t.Errorf(".git changed")
	}
	if now, _ := os.ReadFile(filepath.Join(root, ".git", "config")); string(now) != string(cfg) {
		t.Error(".git/config changed")
	}
}
