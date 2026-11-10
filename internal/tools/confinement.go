package tools

import "github.com/shaktsin/umcode/internal/sandbox"

// Confinement names the mechanism that confines a project's shell commands.
type Confinement string

const (
	ConfMicroVM   Confinement = "microvm"
	ConfOSSandbox Confinement = "os-sandbox"
	ConfNone      Confinement = "none"
)

// ShellConfinement picks the single confinement for a project's shell: the
// microVM when the project enables it and it is available; none when it is
// enabled but unavailable (never a silent fall back to the OS sandbox); the OS
// sandbox when the microVM is off and one is detected; otherwise none.
func ShellConfinement(scope *Scope, vmAvailable bool, sb sandbox.Sandbox) Confinement {
	switch {
	case scope == nil:
		return ConfNone
	case scope.UseCompute && vmAvailable:
		return ConfMicroVM
	case scope.UseCompute:
		return ConfNone
	case sb != nil:
		return ConfOSSandbox
	}
	return ConfNone
}
