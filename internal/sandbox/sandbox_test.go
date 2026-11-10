package sandbox

import (
	"os"
	"os/exec"
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

func TestBwrapArgsReadOnlyRootUsesRoBind(t *testing.T) {
	root := t.TempDir()
	tmp := t.TempDir()
	p := Policy{Root: root, Readable: []string{root, tmp}, Writable: []string{tmp}, RootReadOnly: true}
	args := strings.Join(bwrapArgs(p, root), " ")
	if !strings.Contains(args, "--ro-bind "+root+" "+root) {
		t.Errorf("root must be bound read-only: %s", args)
	}
	if strings.Contains(args, "--bind "+root+" "+root) {
		t.Errorf("root must not be bound writable: %s", args)
	}
	if !strings.Contains(args, "--bind "+tmp+" "+tmp) {
		t.Errorf("writable temporary must stay writable: %s", args)
	}
	p.RootReadOnly = false
	if args := strings.Join(bwrapArgs(p, root), " "); !strings.Contains(args, "--bind "+root+" "+root) {
		t.Errorf("default policy must keep the root writable: %s", args)
	}
}

func TestSeatbeltProfileReadOnlyRootOmitsRootWrite(t *testing.T) {
	p := Policy{Root: "/work/proj", Writable: []string{"/private/tmp/x"}, RootReadOnly: true}
	prof, err := seatbeltProfile(p)
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(prof, "(allow file-write*")
	if i < 0 {
		t.Fatalf("no write allow rule:\n%s", prof)
	}
	rules := prof[i:]
	if strings.Contains(rules, `(subpath "/work/proj")`) {
		t.Errorf("root must not be writable:\n%s", prof)
	}
	if !strings.Contains(rules, `(subpath "/private/tmp/x")`) {
		t.Errorf("temporary must stay writable:\n%s", prof)
	}
}

func TestReadOnlyChildPolicyKeepsReadableAndTemporaryWritable(t *testing.T) {
	root := t.TempDir()
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git", "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := ReadOnlyChildPolicy(root, false, tmp)
	iso := IsolatedChildPolicy(root, false, tmp)
	if !p.RootReadOnly || iso.RootReadOnly {
		t.Fatalf("RootReadOnly: readonly=%v isolated=%v", p.RootReadOnly, iso.RootReadOnly)
	}
	if len(p.Writable) != 1 || p.Writable[0] != realPath(tmp) {
		t.Errorf("only the temporary may be writable: %v", p.Writable)
	}
	if strings.Join(p.Readable, "\n") != strings.Join(iso.Readable, "\n") {
		t.Errorf("readable set must match the isolated policy:\n%v\n%v", p.Readable, iso.Readable)
	}
	var gitRO bool
	for _, ro := range p.ReadOnlyInRoot {
		if ro == filepath.Join(realPath(root), ".git") {
			gitRO = true
		}
	}
	if !gitRO {
		t.Errorf("whole .git must be read-only: %v", p.ReadOnlyInRoot)
	}
}

func TestAgentWritePolicyProtectsWholeDotGit(t *testing.T) {
	root := t.TempDir()
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := AgentWritePolicy(root, false, tmp)
	if p.RootReadOnly {
		t.Error("write-capable agent keeps a writable root")
	}
	if len(p.ReadOnlyInRoot) != 1 || p.ReadOnlyInRoot[0] != filepath.Join(realPath(root), ".git") {
		t.Errorf("ReadOnlyInRoot = %v", p.ReadOnlyInRoot)
	}
	// The lead's policies keep their existing behaviour.
	if d := DefaultPolicy(root, false); len(d.ReadOnlyInRoot) != 0 && strings.HasSuffix(d.ReadOnlyInRoot[0], ".git") {
		t.Errorf("DefaultPolicy must not protect all of .git: %v", d.ReadOnlyInRoot)
	}
	if iso := IsolatedChildPolicy(root, false, tmp); len(iso.ReadOnlyInRoot) != 0 && strings.HasSuffix(iso.ReadOnlyInRoot[0], ".git") {
		t.Errorf("IsolatedChildPolicy must not protect all of .git: %v", iso.ReadOnlyInRoot)
	}
}

func TestSelfCheckReadOnlyRequiresSandbox(t *testing.T) {
	if err := SelfCheckReadOnly(nil, t.TempDir()); err == nil {
		t.Fatal("nil sandbox must fail the self-check")
	}
}

// worktreeFixture creates a repository with a linked worktree, like an agent
// checkout: <wt>/.git is a file pointing outside the root.
func worktreeFixture(t *testing.T) (main, wt string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	main = realPath(t.TempDir())
	wt = filepath.Join(realPath(t.TempDir()), "wt")
	for _, a := range [][]string{{"init", "-q"}, {"-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "--allow-empty", "-m", "x"}, {"worktree", "add", "-q", wt}} {
		c := exec.Command("git", a...)
		c.Dir = main
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", a, err, out)
		}
	}
	return main, wt
}

func TestAgentPoliciesProtectWorktreeGitdirAndCommondir(t *testing.T) {
	main, wt := worktreeFixture(t)
	tmp := t.TempDir()
	gitdir, _ := filepath.EvalSymlinks(filepath.Join(main, ".git", "worktrees", "wt"))
	common := filepath.Join(main, ".git")
	for name, p := range map[string]Policy{"write": AgentWritePolicy(wt, false, tmp), "readonly": ReadOnlyChildPolicy(wt, false, tmp)} {
		has := func(list []string, s string) bool {
			for _, x := range list {
				if x == s {
					return true
				}
			}
			return false
		}
		for _, d := range []string{gitdir, common} {
			if !has(p.ReadOnlyPaths, d) || !has(p.Readable, d) {
				t.Errorf("%s: %s must be readable and read-only: ro=%v readable=%v", name, d, p.ReadOnlyPaths, p.Readable)
			}
		}
		if !has(p.ReadOnlyInRoot, filepath.Join(wt, ".git")) {
			t.Errorf("%s: .git pointer file must be read-only: %v", name, p.ReadOnlyInRoot)
		}
		args := bwrapArgs(p, wt)
		joined := strings.Join(args, " ")
		last := -1
		for i, a := range args {
			if a == "--bind" && args[i+1] == p.Root {
				last = i
			}
		}
		for _, d := range []string{gitdir, common} {
			at := strings.LastIndex(joined, "--ro-bind "+d+" "+d)
			if at < 0 {
				t.Errorf("%s: bwrap lacks ro-bind of %s: %s", name, d, joined)
			} else if last >= 0 && at < strings.Index(joined, "--bind "+p.Root+" "+p.Root) {
				t.Errorf("%s: %s must be bound after the writable root", name, d)
			}
			if strings.Contains(joined, "--bind "+d+" "+d) && !strings.Contains(joined, "--ro-bind "+d+" "+d) {
				t.Errorf("%s: %s bound writable", name, d)
			}
		}
		prof, err := seatbeltProfile(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range []string{gitdir, common} {
			deny := `(deny file-write* (subpath "` + d + `"))`
			if !strings.Contains(prof, deny) || strings.Index(prof, deny) < strings.Index(prof, "(allow file-write*") {
				t.Errorf("%s: seatbelt must deny writes to %s after the allow:\n%s", name, d, prof)
			}
			if !strings.Contains(prof, `(subpath "`+d+`")`) {
				t.Errorf("%s: seatbelt lacks %s", name, d)
			}
		}
	}
}

func TestAgentWritePolicyMissingGitDeniedInSeatbelt(t *testing.T) {
	root := t.TempDir()
	p := AgentWritePolicy(root, false, t.TempDir())
	want := filepath.Join(realPath(root), ".git")
	if len(p.ReadOnlyInRoot) != 1 || p.ReadOnlyInRoot[0] != want {
		t.Fatalf("missing .git must still be listed: %v", p.ReadOnlyInRoot)
	}
	prof, err := seatbeltProfile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prof, `(deny file-write* (subpath "`+want+`"))`) {
		t.Errorf("seatbelt must deny a missing .git:\n%s", prof)
	}
	if strings.Contains(strings.Join(bwrapArgs(p, root), " "), want) {
		t.Error("bwrap cannot mount a missing path; it must be skipped")
	}
}
