package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSeatbeltProfile(t *testing.T) {
	p := Policy{Root: "/work/proj", Network: false, Writable: []string{"/private/tmp"},
		DenyRead: []string{"/Users/a/.ssh"}, ReadOnlyInRoot: []string{"/work/proj/.git/hooks"}}
	prof, err := seatbeltProfile(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"(allow default)", "(deny network*)", "(deny file-write*)",
		`(subpath "/work/proj")`, `(subpath "/private/tmp")`,
		`(deny file-write* (subpath "/work/proj/.git/hooks"))`,
		`(deny file-read* (subpath "/Users/a/.ssh"))`,
	} {
		if !strings.Contains(prof, want) {
			t.Errorf("profile missing %q:\n%s", want, prof)
		}
	}
	// The narrow allow must come after the broad deny (last match wins).
	if strings.Index(prof, "(deny file-write*)") > strings.Index(prof, "(allow file-write*") {
		t.Error("allow must follow the broad deny")
	}
	p.Network = true
	prof, _ = seatbeltProfile(p)
	if strings.Contains(prof, "(deny network*)") {
		t.Error("network should be allowed")
	}
}

func TestSeatbeltQuoting(t *testing.T) {
	q, err := sbplQuote(`/a "b"/c\d`)
	if err != nil || q != `"/a \"b\"/c\\d"` {
		t.Fatalf("quote = %s, %v", q, err)
	}
	if _, err := sbplQuote("/a\nb"); err == nil {
		t.Error("newline in path accepted")
	}
	if _, err := seatbeltProfile(Policy{Root: "/ok", Writable: []string{"/bad\npath"}}); err == nil {
		t.Error("bad writable path accepted")
	}
}

func TestBwrapArgs(t *testing.T) {
	root := t.TempDir()
	hooks := filepath.Join(root, ".git", "hooks")
	os.MkdirAll(hooks, 0o755)
	ssh := t.TempDir()
	args := bwrapArgs(Policy{Root: root, Network: false, DenyRead: []string{ssh}, ReadOnlyInRoot: []string{hooks}}, root)
	joined := strings.Join(args, " ")
	for _, want := range []string{"--unshare-net", "--ro-bind / /", "--bind " + root + " " + root, "--tmpfs " + ssh, "--ro-bind " + hooks + " " + hooks, "--chdir " + root, "--die-with-parent"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q: %s", want, joined)
		}
	}
	if strings.Index(joined, "--bind "+root) > strings.Index(joined, "--ro-bind "+hooks) {
		t.Error("read-only overlay must come after the writable bind")
	}
	if strings.Contains(strings.Join(bwrapArgs(Policy{Root: root, Network: true}, ""), " "), "--unshare-net") {
		t.Error("network allowed but --unshare-net set")
	}
}

func TestDefaultPolicy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".npm"), 0o755)
	os.MkdirAll(filepath.Join(home, ".ssh"), 0o755)
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".git", "hooks"), 0o755)

	off := DefaultPolicy(root, false)
	for _, w := range off.Writable {
		if strings.HasPrefix(w, realPath(home)) {
			t.Errorf("cache dir %s writable without network", w)
		}
	}
	on := DefaultPolicy(root, true)
	found := false
	for _, w := range on.Writable {
		found = found || w == realPath(filepath.Join(home, ".npm"))
	}
	if !found {
		t.Errorf("npm cache not writable with network: %v", on.Writable)
	}
	if len(on.DenyRead) != 1 || on.DenyRead[0] != realPath(filepath.Join(home, ".ssh")) {
		t.Errorf("deny read = %v", on.DenyRead)
	}
	if len(on.ReadOnlyInRoot) != 1 || !strings.HasSuffix(on.ReadOnlyInRoot[0], filepath.Join(".git", "hooks")) {
		t.Errorf("read-only in root = %v", on.ReadOnlyInRoot)
	}
	if on.Root != realPath(root) {
		t.Errorf("root = %s", on.Root)
	}
}

func TestDefaultPolicyLimitsReadsAndSharedCaches(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, rel := range []string{"Documents", ".cache/private-app", "Library/Caches/private-app", ".cache/go-build", ".nvm/versions/node/v24/bin"} {
		if err := os.MkdirAll(filepath.Join(home, rel), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	p := DefaultPolicy(root, false)
	if p.Readable == nil {
		t.Fatal("ordinary commands still have unrestricted reads")
	}
	for _, allowed := range p.Readable {
		for _, forbidden := range []string{home, filepath.Join(home, "Documents"), filepath.Join(home, ".cache"), filepath.Join(home, "Library")} {
			if allowed == realPath(forbidden) {
				t.Errorf("overbroad read root: %s", allowed)
			}
		}
	}
	for _, want := range []string{root, filepath.Join(home, ".cache/go-build"), filepath.Join(home, ".nvm/versions/node")} {
		if !containsPath(p.Readable, realPath(want)) {
			t.Errorf("required read root %s missing: %v", want, p.Readable)
		}
	}
	for _, writable := range DefaultPolicy(root, true).Writable {
		if writable == realPath(filepath.Join(home, ".cache")) || writable == realPath(filepath.Join(home, "Library/Caches")) {
			t.Errorf("shared app cache writable: %s", writable)
		}
	}
}

func TestDefaultPolicyIncludesSpecificPythonFrameworks(t *testing.T) {
	p := DefaultPolicy(t.TempDir(), false)
	for _, framework := range []string{
		"/Library/Frameworks/Python.framework",
		"/opt/homebrew/Frameworks/Python.framework",
		"/usr/local/Frameworks/Python.framework",
	} {
		// Cover the known-runtime contract even on hosts without every Python
		// distribution installed; installed roots must reach the real policy.
		if !containsPath(commandRuntimePaths, framework) {
			t.Errorf("standard Python framework missing from runtime policy: %s", framework)
		}
		if exists(framework) && !containsPath(p.Readable, realPath(framework)) {
			t.Errorf("installed Python framework is unreadable: %s", framework)
		}
		if containsPath(commandRuntimePaths, filepath.Dir(framework)) || containsPath(p.Readable, filepath.Dir(framework)) {
			t.Errorf("unrelated frameworks granted access: %s", filepath.Dir(framework))
		}
	}
}

func containsPath(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}

func TestBwrapDefaultPolicyUsesReadAllowlist(t *testing.T) {
	root := t.TempDir()
	args := bwrapArgs(DefaultPolicy(root, false), root)
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--ro-bind / /") {
		t.Fatal("ordinary Linux commands can read the entire host")
	}
	if !strings.Contains(joined, "--tmpfs /") || !strings.Contains(joined, "--ro-bind "+realPath(root)+" "+realPath(root)) {
		t.Fatalf("project not mounted in restricted filesystem: %s", joined)
	}
}

func TestBwrapReadablePolicyRetainsPrivateTemporaryDirectory(t *testing.T) {
	root := t.TempDir()
	args := bwrapArgs(Policy{Root: root, Readable: []string{root, "/tmp"}, Writable: []string{"/tmp"}}, root)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--tmpfs /tmp") || strings.Contains(joined, "--ro-bind /tmp /tmp") || strings.Contains(joined, "--bind /tmp /tmp") {
		t.Fatalf("temporary directory is missing or shared instead of private: %s", joined)
	}
}

func TestBwrapReadablePolicyResolvesCommandDirectory(t *testing.T) {
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "project-link")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	args := bwrapArgs(DefaultPolicy(alias, false), alias)
	if args[len(args)-1] != realPath(root) {
		t.Fatalf("command directory points outside canonical project mount: %v", args[len(args)-2:])
	}
}
