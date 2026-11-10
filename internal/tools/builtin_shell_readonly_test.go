package tools

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/shaktsin/umcode/internal/compute"
	"github.com/shaktsin/umcode/internal/sandbox"
)

type policySandbox struct {
	mu       sync.Mutex
	policies []sandbox.Policy
}

func (*policySandbox) Name() string { return "fake" }
func (p *policySandbox) Wrap(cmd *exec.Cmd, pol sandbox.Policy) error {
	p.mu.Lock()
	p.policies = append(p.policies, pol)
	p.mu.Unlock()
	return nil
}

type recordCompute struct{ reqs []compute.Request }

func (r *recordCompute) Run(_ context.Context, req compute.Request) (int, error) {
	r.reqs = append(r.reqs, req)
	return 0, nil
}

func agentScope(root string, mod func(*Scope)) context.Context {
	s := &Scope{ThreadID: "t", ProjectName: "demo", Root: root, RestrictedChild: true, RuntimeAgent: true, Confined: true, ReadOnly: true}
	if mod != nil {
		mod(s)
	}
	return WithScope(context.Background(), s)
}

var echoArgs = json.RawMessage(`{"command":"true"}`)

func TestShellReadOnlyScopeRunsUnderReadOnlyPolicy(t *testing.T) {
	root := t.TempDir()
	sb := &policySandbox{}
	sh := &shellRun{sandbox: sb}
	ctx := agentScope(root, func(s *Scope) { s.ShellReadOnly = true })
	if _, err := sh.Call(ctx, echoArgs); err != nil {
		t.Fatal(err)
	}
	if len(sb.policies) != 1 || !sb.policies[0].RootReadOnly {
		t.Fatalf("policies = %+v", sb.policies)
	}
	// read-write agent: writable root, .git still protected.
	sb2 := &policySandbox{}
	sh2 := &shellRun{sandbox: sb2}
	ctx = agentScope(root, func(s *Scope) { s.ReadOnly = false })
	if _, err := sh2.Call(ctx, echoArgs); err != nil {
		t.Fatal(err)
	}
	if len(sb2.policies) != 1 || sb2.policies[0].RootReadOnly || len(sb2.policies[0].ReadOnlyInRoot) == 0 {
		t.Fatalf("write policy = %+v", sb2.policies)
	}
	if sb2.policies[0].Network {
		t.Fatal("agent network must come only from the scope")
	}
}

func TestShellReadOnlyInMicroVMRequestsReadOnlyRoot(t *testing.T) {
	vm := &recordCompute{}
	sh := &shellRun{compute: vm}
	ctx := agentScope(t.TempDir(), func(s *Scope) { s.UseCompute = true; s.ShellReadOnly = true })
	if _, err := sh.Call(ctx, echoArgs); err != nil {
		t.Fatal(err)
	}
	if len(vm.reqs) != 1 || !vm.reqs[0].RootReadOnly {
		t.Fatalf("reqs = %+v", vm.reqs)
	}
	vm2 := &recordCompute{}
	sh2 := &shellRun{compute: vm2}
	ctx = agentScope(t.TempDir(), func(s *Scope) { s.UseCompute = true; s.ReadOnly = false })
	if _, err := sh2.Call(ctx, echoArgs); err != nil || vm2.reqs[0].RootReadOnly {
		t.Fatalf("read-write VM request = %+v err %v", vm2.reqs, err)
	}
}

func TestShellReadOnlyWithoutSandboxRefuses(t *testing.T) {
	sh := &shellRun{}
	ctx := agentScope(t.TempDir(), func(s *Scope) { s.ShellReadOnly = true })
	_, err := sh.Call(ctx, echoArgs)
	if err == nil || !strings.Contains(err.Error(), "no sandbox available") {
		t.Fatalf("err = %v", err)
	}
}

func TestRestrictedAgentNeverRunsShellOnHostWhenVMChosen(t *testing.T) {
	sb := &policySandbox{}
	sh := &shellRun{sandbox: sb} // VM toggle on, VM down (no runner), sandbox present
	ctx := agentScope(t.TempDir(), func(s *Scope) { s.UseCompute = true; s.ReadOnly = false })
	marker := t.TempDir() + "/ran"
	args, _ := json.Marshal(map[string]string{"command": "touch " + marker})
	_, err := sh.Call(ctx, args)
	if err == nil || !strings.Contains(err.Error(), "microVM is unavailable") {
		t.Fatalf("err = %v", err)
	}
	if len(sb.policies) != 0 {
		t.Fatal("sandbox/host exec was reached")
	}
	if _, statErr := exec.Command("test", "-e", marker).CombinedOutput(); statErr == nil {
		t.Fatal("command ran on the host")
	}
}

func TestAgentShellNotConfinedRefuses(t *testing.T) {
	sh := &shellRun{sandbox: &policySandbox{}}
	ctx := agentScope(t.TempDir(), func(s *Scope) { s.Confined = false })
	if _, err := sh.Call(ctx, echoArgs); err == nil {
		t.Fatal("unconfined agent shell ran")
	}
}
