package sandbox

import (
	"context"
	"os"
	"os/exec"
	"sync"
	"time"
)

// bwrap runs commands under bubblewrap.
type bwrap struct{ exe string }

func (bwrap) Name() string { return "bwrap" }

func (b bwrap) Wrap(cmd *exec.Cmd, p Policy) error {
	args := bwrapArgs(p, cmd.Dir)
	inner := append([]string{cmd.Path}, cmd.Args[1:]...)
	cmd.Path = b.exe
	cmd.Args = append(append([]string{b.exe}, args...), append([]string{"--"}, inner...)...)
	return nil
}

// bwrapArgs builds bubblewrap's arguments: the whole filesystem read-only,
// then the writable locations bound on top, an empty view of credential
// directories, and no network unless the policy allows it.
func bwrapArgs(p Policy, dir string) []string {
	args := []string{"--die-with-parent", "--new-session", "--unshare-pid",
		"--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--tmpfs", "/tmp"}
	if !p.Network {
		args = append(args, "--unshare-net")
	}
	for _, w := range append(append([]string{}, p.Writable...), p.Root) {
		if w != "/tmp" && isDir(w) {
			args = append(args, "--bind", w, w)
		}
	}
	for _, d := range p.DenyRead {
		if isDir(d) {
			args = append(args, "--tmpfs", d)
		} else if exists(d) {
			args = append(args, "--ro-bind", "/dev/null", d)
		}
	}
	for _, ro := range p.ReadOnlyInRoot {
		if exists(ro) {
			args = append(args, "--ro-bind", ro, ro)
		}
	}
	if dir != "" {
		args = append(args, "--chdir", dir)
	}
	return args
}

var (
	bwrapOnce sync.Once
	bwrapOK   bool
)

// bwrapUsable reports whether bubblewrap can create the namespaces it needs
// here; unprivileged user namespaces are disabled on some systems.
func bwrapUsable(exe string) bool {
	bwrapOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, exe, "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--unshare-pid", "true")
		cmd.Env = os.Environ()
		bwrapOK = cmd.Run() == nil
	})
	return bwrapOK
}
