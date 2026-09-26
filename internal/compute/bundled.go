package compute

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// BundledRunner talks to UMCode's private compute bridge packaged inside
// the desktop app. The bridge owns the libkrun ABI and VM lifecycle so the
// Go engine does not depend on a system-installed CLI or shared library.
type BundledRunner struct {
	RuntimeDir string
	Executable string
	Start      func(context.Context, string, ...string) *exec.Cmd
	checkHost  func() error
}

func NewBundledRunner() *BundledRunner { return &BundledRunner{} }

type GuestRequest struct {
	Version   int    `json:"version"`
	RootFS    string `json:"rootfs"`
	Root      string `json:"root"`
	Dir       string `json:"dir"`
	Command   string `json:"command"`
	Network   bool   `json:"network"`
	Timeout   int    `json:"timeoutSeconds,omitempty"`
	VCPUs     int    `json:"vcpus"`
	MemoryMiB int    `json:"memoryMiB"`
	HostPort  int    `json:"hostPort,omitempty"`
	GuestPort int    `json:"guestPort,omitempty"`
}

func (r GuestRequest) Validate() error {
	if r.Version != 1 {
		return fmt.Errorf("unsupported compute request version %d", r.Version)
	}
	root, err := filepath.EvalSymlinks(r.Root)
	if err != nil {
		return fmt.Errorf("resolve project root: %w", err)
	}
	dir, err := filepath.EvalSymlinks(r.Dir)
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("compute working directory must be inside the project")
	}
	if strings.TrimSpace(r.Command) == "" {
		return errors.New("compute command cannot be empty")
	}
	if r.VCPUs < 1 || r.VCPUs > 8 || r.MemoryMiB < 512 || r.MemoryMiB > 8192 {
		return errors.New("compute resource limits are outside the supported range")
	}
	if (r.HostPort == 0) != (r.GuestPort == 0) || r.HostPort < 0 || r.HostPort > 65535 || r.GuestPort < 0 || r.GuestPort > 65535 {
		return errors.New("compute port mapping must contain valid host and guest ports")
	}
	rootfs, err := filepath.EvalSymlinks(r.RootFS)
	if err != nil {
		return fmt.Errorf("resolve bundled guest root: %w", err)
	}
	if !filepath.IsAbs(rootfs) {
		return errors.New("bundled guest root must be an absolute path")
	}
	for _, required := range []string{"bin/sh", "bin/mount"} {
		if err := validateGuestExecutable(rootfs, required); err != nil {
			return fmt.Errorf("bundled guest root is missing required %s: %w", required, err)
		}
	}
	return nil
}

type bundledPreview struct {
	hostPort int
	cancel   context.CancelFunc
	done     chan error
}

func (p *bundledPreview) HostPort() int { return p.hostPort }
func (p *bundledPreview) Wait() error   { return <-p.done }
func (p *bundledPreview) Stop()         { p.cancel() }

// StartPreview launches a persistent guest process and exposes exactly one
// TCP port on host loopback. Preview currently requires the project's network
// permission because libkrun 1.x uses the same TSI device for port forwarding.
func (b *BundledRunner) StartPreview(ctx context.Context, r Request, guestPort int) (PreviewProcess, error) {
	if guestPort < 1 || guestPort > 65535 {
		return nil, errors.New("preview port must be between 1 and 65535")
	}
	if !r.Network {
		return nil, errors.New("live preview requires network access for this project with the bundled libkrun runtime")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("reserve preview port: %w", err)
	}
	hostPort := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	checkHost := b.checkHost
	if checkHost == nil {
		checkHost = CheckHostAvailability
	}
	if err := checkHost(); err != nil {
		return nil, err
	}
	req, err := validateRequest(r)
	if err != nil {
		return nil, err
	}
	req.HostPort, req.GuestPort = hostPort, guestPort
	path, err := b.bridgePath()
	if err != nil {
		return nil, err
	}
	rootfs, cleanup, err := prepareGuestRoot(filepath.Join(filepath.Dir(path), "rootfs"))
	if err != nil {
		return nil, err
	}
	req.RootFS = rootfs
	limit, err := diskLimit(r.DiskLimitBytes)
	if err != nil {
		cleanup()
		return nil, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	stopWatch := make(chan struct{})
	diskExceeded := make(chan error, 1)
	go watchDiskUsage(runCtx, stopWatch, cancel, req.Root, limit, diskExceeded)
	start := b.Start
	if start == nil {
		start = exec.CommandContext
	}
	cmd := start(runCtx, path, "run", "--request-stdin")
	libDir := filepath.Join(filepath.Dir(path), "lib")
	cmd.Dir = libDir
	cmd.Env = []string{
		"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + os.TempDir(), "LANG=C.UTF-8",
		"DYLD_LIBRARY_PATH=" + libDir, "LD_LIBRARY_PATH=" + libDir,
	}
	cmd.Stdin, err = jsonReader(req)
	if err != nil {
		cancel()
		cleanup()
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = r.Stdout, r.Stderr
	if err := cmd.Start(); err != nil {
		cancel()
		close(stopWatch)
		cleanup()
		return nil, fmt.Errorf("start bundled preview microVM: %w", err)
	}
	p := &bundledPreview{hostPort: hostPort, cancel: cancel, done: make(chan error, 1)}
	go func() {
		err := cmd.Wait()
		close(stopWatch)
		cleanup()
		diskFailure := false
		select {
		case diskErr := <-diskExceeded:
			err = diskErr
			diskFailure = true
		default:
		}
		if !diskFailure && runCtx.Err() != nil && err == nil {
			err = runCtx.Err()
		} else if !diskFailure && errors.Is(runCtx.Err(), context.Canceled) && err != nil {
			err = context.Canceled
		}
		p.done <- err
		close(p.done)
	}()
	return p, nil
}

func diskLimit(limit int64) (int64, error) {
	if limit == 0 {
		limit = 2 << 30
	}
	if limit < 1<<20 {
		return 0, errors.New("compute disk limit must be at least 1 MiB")
	}
	if limit > 16<<30 {
		return 0, errors.New("compute disk limit cannot exceed 16384 MiB")
	}
	return limit, nil
}

// watchDiskUsage is a protective ceiling on the task workspace, checked while
// the guest is running. It does not follow symlinks and cancels the VM when the
// checkout exceeds its private disk budget.
func watchDiskUsage(ctx context.Context, stop <-chan struct{}, cancel context.CancelFunc, root string, limit int64, exceeded chan<- error) {
	check := func() bool {
		size, err := directorySize(root)
		if err == nil && size > limit {
			select {
			case exceeded <- fmt.Errorf("task workspace exceeded its %d MiB disk limit; the microVM was stopped", limit>>20):
			default:
			}
			cancel()
			return true
		}
		return false
	}
	if check() {
		return
	}
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-tick.C:
			if check() {
				return
			}
		}
	}
}

func directorySize(root string) (int64, error) {
	var size int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			size += info.Size()
		}
		return nil
	})
	return size, err
}

func validateRequest(r Request) (GuestRequest, error) {
	root, err := filepath.EvalSymlinks(r.Root)
	if err != nil {
		return GuestRequest{}, fmt.Errorf("resolve project root: %w", err)
	}
	dir, err := filepath.EvalSymlinks(r.Dir)
	if err != nil {
		return GuestRequest{}, fmt.Errorf("resolve working directory: %w", err)
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return GuestRequest{}, errors.New("compute working directory must be inside the project")
	}
	if strings.TrimSpace(r.Command) == "" {
		return GuestRequest{}, errors.New("compute command cannot be empty")
	}
	resources, err := validateResources(r.VCPUs, r.MemoryMiB)
	if err != nil {
		return GuestRequest{}, err
	}
	return GuestRequest{Version: 1, Root: root, Dir: dir, Command: r.Command, Network: r.Network,
		Timeout: r.Timeout, VCPUs: resources.VCPUs, MemoryMiB: resources.MemoryMiB}, nil
}

func validateResources(vcpus, memoryMiB int) (GuestRequest, error) {
	if vcpus == 0 {
		vcpus = 4
	}
	if memoryMiB == 0 {
		memoryMiB = 4096
	}
	if vcpus < 1 || vcpus > 8 {
		return GuestRequest{}, errors.New("compute vCPU limit must be between 1 and 8")
	}
	if memoryMiB < 512 || memoryMiB > 8192 {
		return GuestRequest{}, errors.New("compute memory limit must be between 512 and 8192 MiB")
	}
	return GuestRequest{VCPUs: vcpus, MemoryMiB: memoryMiB}, nil
}

func (b *BundledRunner) bridgePath() (string, error) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return "", errors.New("bundled microVM compute currently supports macOS and Linux only")
	}
	if b.Executable != "" {
		return b.Executable, nil
	}
	dir := b.RuntimeDir
	if dir == "" {
		exe, err := os.Executable()
		if err != nil {
			return "", err
		}
		// macOS: Contents/Resources/umcode -> Contents/Resources/compute.
		// Linux: the private bridge sits beside the packaged engine directory.
		dir = filepath.Join(filepath.Dir(exe), "compute")
	}
	name := "umcode-compute"
	path := filepath.Join(dir, name)
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("UMCode's bundled libkrun compute runtime is missing at %s; install a complete UMCode app build", path)
	}
	if info.IsDir() || info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("bundled compute runtime is not executable: %s", path)
	}
	return path, nil
}

func (b *BundledRunner) Run(ctx context.Context, r Request) (int, error) {
	checkHost := b.checkHost
	if checkHost == nil {
		checkHost = CheckHostAvailability
	}
	if err := checkHost(); err != nil {
		return -1, err
	}
	req, err := validateRequest(r)
	if err != nil {
		return -1, err
	}
	path, err := b.bridgePath()
	if err != nil {
		return -1, err
	}
	rootfs, cleanup, err := prepareGuestRoot(filepath.Join(filepath.Dir(path), "rootfs"))
	if err != nil {
		return -1, err
	}
	defer cleanup()
	req.RootFS = rootfs
	start := b.Start
	if start == nil {
		start = exec.CommandContext
	}
	limit, err := diskLimit(r.DiskLimitBytes)
	if err != nil {
		return -1, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopWatch := make(chan struct{})
	diskExceeded := make(chan error, 1)
	go watchDiskUsage(runCtx, stopWatch, cancel, req.Root, limit, diskExceeded)
	cmd := start(runCtx, path, "run", "--request-stdin")
	libDir := filepath.Join(filepath.Dir(path), "lib")
	cmd.Dir = libDir
	cmd.Env = []string{
		"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + os.TempDir(), "LANG=C.UTF-8",
		"DYLD_LIBRARY_PATH=" + libDir, "LD_LIBRARY_PATH=" + libDir,
	}
	cmd.Stdin, err = jsonReader(req)
	if err != nil {
		return -1, err
	}
	cmd.Stdout, cmd.Stderr = r.Stdout, r.Stderr
	if err := cmd.Run(); err != nil {
		select {
		case diskErr := <-diskExceeded:
			close(stopWatch)
			return -1, diskErr
		default:
		}
		close(stopWatch)
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode(), nil
		}
		if ctx.Err() != nil {
			return -1, ctx.Err()
		}
		return -1, fmt.Errorf("bundled libkrun microVM failed: %w", err)
	}
	close(stopWatch)
	select {
	case diskErr := <-diskExceeded:
		return -1, diskErr
	default:
	}
	return 0, nil
}

func jsonReader(req GuestRequest) (io.Reader, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	return strings.NewReader(string(data)), nil
}
