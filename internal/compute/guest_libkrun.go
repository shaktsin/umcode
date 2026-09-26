//go:build libkrun && (darwin || linux)

package compute

/*
#cgo LDFLAGS: -lkrun
#include <libkrun.h>
#include <errno.h>
#include <stdio.h>
#include <stdlib.h>

static int32_t umcode_start_guest(const char *rootfs, const char *project,
                                    const char *workdir, const char *command,
                                    const char *resolver,
                                    bool allow_network, int32_t vcpus, int32_t memory_mib,
                                    int32_t host_port, int32_t guest_port) {
    int32_t ctx = krun_create_ctx();
    if (ctx < 0) return ctx;
#define CHECK_KRUN(call) do { int32_t rc = (call); if (rc < 0) { fprintf(stderr, "libkrun setup failed at %s (error %d)\n", #call, rc); krun_free_ctx((uint32_t)ctx); return rc; } } while (0)
    CHECK_KRUN(krun_set_vm_config((uint32_t)ctx, vcpus, memory_mib));
    CHECK_KRUN(krun_add_virtio_console_default((uint32_t)ctx, 0, 1, 2));
    CHECK_KRUN(krun_set_root((uint32_t)ctx, rootfs));
    CHECK_KRUN(krun_add_virtiofs3((uint32_t)ctx, "umcode_workspace", project, 0, false));
    CHECK_KRUN(krun_disable_implicit_vsock((uint32_t)ctx));
    if (allow_network) {
        CHECK_KRUN(krun_add_vsock((uint32_t)ctx, KRUN_TSI_HIJACK_INET));
    }
    if (host_port > 0 && guest_port > 0) {
        char mapping[32];
        snprintf(mapping, sizeof(mapping), "%d:%d", host_port, guest_port);
        const char *port_map[] = {mapping, NULL};
        CHECK_KRUN(krun_set_port_map((uint32_t)ctx, port_map));
    }
    CHECK_KRUN(krun_set_workdir((uint32_t)ctx, "/"));

    const char *guest_script =
        "mkdir -p /workspace && "
        "mount -t virtiofs umcode_workspace /workspace && "
        "workdir=$(printf '%s' \"$1\" | base64 -d) && "
        "command=$(printf '%s' \"$2\" | base64 -d) && "
        "if [ -n \"$3\" ]; then printf '%s' \"$3\" | base64 -d > /etc/resolv.conf; fi && "
        "cd -- \"$workdir\" && exec /bin/sh -c \"$command\"";
    const char *guest_argv[] = {"-c", guest_script, "umcode-compute", workdir, command, resolver, NULL};
    const char *guest_env[] = {
        "HOME=/tmp", "TMPDIR=/tmp", "PATH=/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin",
        "LANG=C.UTF-8", NULL
    };
    CHECK_KRUN(krun_set_exec((uint32_t)ctx, "/bin/sh", guest_argv, guest_env));
    int32_t rc = krun_start_enter((uint32_t)ctx);
    if (rc < 0) fprintf(stderr, "libkrun guest start failed at krun_start_enter (error %d)\n", rc);
    return rc;
#undef CHECK_KRUN
}
*/
import "C"

import (
	"encoding/base64"
	"fmt"
	"path/filepath"
	"runtime"
	"unsafe"
)

func RunGuest(req GuestRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	root, err := filepath.EvalSymlinks(req.Root)
	if err != nil {
		return err
	}
	dir, err := filepath.EvalSymlinks(req.Dir)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return err
	}
	guestDir := "/workspace"
	if rel != "." {
		guestDir = filepath.ToSlash(filepath.Join(guestDir, rel))
	}
	cRootFS := C.CString(req.RootFS)
	cRoot := C.CString(root)
	// libkrun 1.x places argv in a kernel command-line structure that rejects
	// control and non-ASCII bytes. Commands are commonly multiline, and project
	// paths may contain Unicode, so transport both as base64 and decode in-guest.
	cGuestDir := C.CString(base64.StdEncoding.EncodeToString([]byte(guestDir)))
	cCommand := C.CString(base64.StdEncoding.EncodeToString([]byte(req.Command)))
	resolver := ""
	if req.Network {
		resolver = base64.StdEncoding.EncodeToString([]byte(guestResolverConfig()))
	}
	cResolver := C.CString(resolver)
	defer C.free(unsafe.Pointer(cRootFS))
	defer C.free(unsafe.Pointer(cRoot))
	defer C.free(unsafe.Pointer(cGuestDir))
	defer C.free(unsafe.Pointer(cCommand))
	defer C.free(unsafe.Pointer(cResolver))
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return fmt.Errorf("libkrun guest execution is unsupported on %s", runtime.GOOS)
	}
	if rc := C.umcode_start_guest(cRootFS, cRoot, cGuestDir, cCommand, cResolver, C.bool(req.Network),
		C.int32_t(req.VCPUs), C.int32_t(req.MemoryMiB), C.int32_t(req.HostPort), C.int32_t(req.GuestPort)); rc < 0 {
		return fmt.Errorf("libkrun failed before guest exit (error %d)", int32(rc))
	}
	return nil
}
