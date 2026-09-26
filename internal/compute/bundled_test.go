package compute

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateRequestContainsOnlyProjectPaths(t *testing.T) {
	root := t.TempDir()
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "src")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	req, err := validateRequest(Request{Root: root, Dir: dir, Command: "go test ./..."})
	if err != nil {
		t.Fatal(err)
	}
	if req.Version != 1 || req.Root != canonicalRoot || req.Dir != filepath.Join(canonicalRoot, "src") || req.Command != "go test ./..." {
		t.Fatalf("unexpected bridge request: %#v", req)
	}
	if _, err := validateRequest(Request{Root: root, Dir: filepath.Dir(root), Command: "pwd"}); err == nil {
		t.Fatal("expected outside working directory to be rejected")
	}
	if _, err := validateRequest(Request{Root: root, Dir: root, Command: "  "}); err == nil {
		t.Fatal("expected empty command to be rejected")
	}
}

func TestBundledRunnerSendsStructuredRequestToPrivateBridge(t *testing.T) {
	root := t.TempDir()
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	bridge := filepath.Join(t.TempDir(), "umcode-compute")
	if err := os.WriteFile(bridge, []byte("test bridge"), 0o755); err != nil {
		t.Fatal(err)
	}
	rootfs := filepath.Join(filepath.Dir(bridge), "rootfs")
	if err := os.MkdirAll(filepath.Join(filepath.Dir(bridge), "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(rootfs, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootfs, "bin", "sh"), []byte("shell"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootfs, "bin", "mount"), []byte("mount"), 0o755); err != nil {
		t.Fatal(err)
	}
	var gotPath string
	var gotArgs []string
	var gotCmd *exec.Cmd
	var output bytes.Buffer
	runner := &BundledRunner{
		Executable: bridge,
		checkHost:  func() error { return nil },
		Start: func(_ context.Context, name string, args ...string) *exec.Cmd {
			gotPath, gotArgs = name, args
			gotCmd = exec.Command("/bin/sh", "-c", "cat")
			return gotCmd
		},
	}
	code, err := runner.Run(context.Background(), Request{Root: root, Dir: root, Command: "pwd", Network: false, Stdout: &output})
	if err != nil || code != 0 {
		t.Fatalf("Run() = %d, %v", code, err)
	}
	if gotPath != bridge || strings.Join(gotArgs, " ") != "run --request-stdin" {
		t.Fatalf("unexpected bridge invocation: %s %v", gotPath, gotArgs)
	}
	if gotCmd.Dir != filepath.Join(filepath.Dir(bridge), "lib") || !strings.Contains(strings.Join(gotCmd.Env, "\n"), "LD_LIBRARY_PATH="+gotCmd.Dir) {
		t.Fatalf("runtime library search path was not isolated to the app bundle: dir=%q env=%v", gotCmd.Dir, gotCmd.Env)
	}
	var wire GuestRequest
	if err := json.Unmarshal(output.Bytes(), &wire); err != nil {
		t.Fatalf("bridge did not receive valid JSON: %v (%q)", err, output.String())
	}
	if wire.Root != canonicalRoot || wire.Dir != canonicalRoot || wire.Command != "pwd" || wire.Network || wire.VCPUs != 4 || wire.MemoryMiB != 4096 {
		t.Fatalf("unexpected bridge request: %#v", wire)
	}
	if _, err := os.Stat(wire.RootFS); !os.IsNotExist(err) {
		t.Fatalf("temporary guest root should be removed after execution, stat err=%v", err)
	}
}

func TestBundledRunnerExplainsMissingAppRuntime(t *testing.T) {
	runner := &BundledRunner{RuntimeDir: t.TempDir()}
	if _, err := runner.bridgePath(); err == nil || !strings.Contains(err.Error(), "bundled libkrun compute runtime is missing") {
		t.Fatalf("expected actionable missing runtime error, got %v", err)
	}
}

func TestBundledRunnerChecksHostBeforeStartingBridge(t *testing.T) {
	called := false
	runner := &BundledRunner{
		checkHost: func() error { return errors.New("test host unavailable") },
		Start: func(context.Context, string, ...string) *exec.Cmd {
			called = true
			return exec.Command("true")
		},
	}
	_, err := runner.Run(context.Background(), Request{Root: t.TempDir(), Dir: t.TempDir(), Command: "true"})
	if err == nil || !strings.Contains(err.Error(), "test host unavailable") {
		t.Fatalf("host preflight error = %v", err)
	}
	if called {
		t.Fatal("bridge started even though host preflight failed")
	}
}

func TestBridgeRequestUsesStableJSONContract(t *testing.T) {
	req := GuestRequest{Version: 1, RootFS: "/runtime/rootfs", Root: "/project", Dir: "/project/src", Command: "go test", Network: true, VCPUs: 4, MemoryMiB: 4096, HostPort: 18000, GuestPort: 8000}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var decoded GuestRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != req {
		t.Fatalf("roundtrip mismatch: %#v != %#v", decoded, req)
	}
}

func TestPreviewRequiresExplicitNetworkPermission(t *testing.T) {
	runner := &BundledRunner{}
	_, err := runner.StartPreview(context.Background(), Request{Command: "npm run dev"}, 5173)
	if err == nil || !strings.Contains(err.Error(), "requires network access") {
		t.Fatalf("expected explicit network permission error, got %v", err)
	}
}

func TestComputeResourcesAreBounded(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		vcpus, memory int
		wantErr       bool
	}{{0, 0, false}, {1, 512, false}, {8, 8192, false}, {9, 4096, true}, {4, 8193, true}, {1, 256, true}} {
		req, err := validateRequest(Request{Root: root, Dir: root, Command: "true", VCPUs: tc.vcpus, MemoryMiB: tc.memory})
		if (err != nil) != tc.wantErr {
			t.Fatalf("resources %d vCPU/%d MiB: req=%+v err=%v", tc.vcpus, tc.memory, req, err)
		}
		if err == nil && (req.VCPUs < 1 || req.MemoryMiB < 512) {
			t.Fatalf("defaults were not applied: %+v", req)
		}
	}
}

func TestDiskLimitCancelsRunningBridge(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "large.bin"), make([]byte, (1<<20)+1), 0o600); err != nil {
		t.Fatal(err)
	}
	bridge := filepath.Join(t.TempDir(), "umcode-compute")
	if err := os.WriteFile(bridge, []byte("test bridge"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(filepath.Dir(bridge), "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	rootfs := filepath.Join(filepath.Dir(bridge), "rootfs")
	if err := os.MkdirAll(filepath.Join(rootfs, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sh", "mount"} {
		if err := os.WriteFile(filepath.Join(rootfs, "bin", name), []byte(name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	runner := &BundledRunner{Executable: bridge, checkHost: func() error { return nil }, Start: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 10")
	}}
	_, err := runner.Run(context.Background(), Request{Root: root, Dir: root, Command: "true", DiskLimitBytes: 1 << 20})
	if err == nil || !strings.Contains(err.Error(), "disk limit") {
		t.Fatalf("disk limit should stop the bridge, got %v", err)
	}
}

func TestPrepareGuestRootRequiresToolsAndReturnsDisposableCopy(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "bin", "sh"), []byte("shell"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepareGuestRoot(base); err == nil || !strings.Contains(err.Error(), "bin/mount") {
		t.Fatalf("expected missing mount tool to be rejected, got %v", err)
	}
	if err := os.WriteFile(filepath.Join(base, "bin", "mount"), []byte("mount"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(base, "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(base, "tmp"), 0o777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	copy, cleanup, err := prepareGuestRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	if copy == base {
		t.Fatal("guest root should be a disposable copy")
	}
	if _, err := os.Stat(filepath.Join(copy, "bin", "mount")); err != nil {
		t.Fatalf("copied guest root is missing mount: %v", err)
	}
	info, err := os.Stat(filepath.Join(copy, "tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o777 || info.Mode()&os.ModeSticky == 0 {
		t.Fatalf("guest temp permissions were not preserved: mode=%v", info.Mode())
	}
	cleanup()
	if _, err := os.Stat(copy); !os.IsNotExist(err) {
		t.Fatalf("guest root copy should be removed after use; stat err=%v", err)
	}
}

func TestGuestRootResolvesAbsoluteSymlinksInsideGuest(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin", "busybox"), []byte("guest binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/bin/busybox", filepath.Join(root, "bin", "sh")); err != nil {
		t.Fatal(err)
	}
	if err := validateGuestExecutable(root, "bin/sh"); err != nil {
		t.Fatalf("Linux absolute symlink should resolve within the guest root: %v", err)
	}
	if err := os.Symlink("/../../host-secret", filepath.Join(root, "bin", "escape")); err != nil {
		t.Fatal(err)
	}
	if err := validateGuestExecutable(root, "bin/escape"); err == nil {
		t.Fatal("expected absolute symlink escaping the guest root to be rejected")
	}
}
