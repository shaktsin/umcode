package plugins

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var pluginNameRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`)
var settingNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func ValidatePackage(pkg Package) []Diagnostic {
	diagnostics := append([]Diagnostic(nil), pkg.Diagnostics...)
	if pkg.ID == "" || len(pkg.ID) > 64 || !pluginNameRE.MatchString(pkg.ID) || strings.Contains(pkg.ID, "..") || strings.Contains(pkg.ID, "--") {
		diagnostics = append(diagnostics, Diagnostic{Code: "validate/invalid_identity", Phase: "validate", Severity: SeverityError, Component: "manifest", Message: fmt.Sprintf("invalid plugin identity %q", pkg.ID), Remediation: "Use lowercase letters, digits, dots, and single hyphens."})
	}
	seen := map[string]bool{}
	for _, skill := range pkg.Skills {
		key := "skill:" + skill.Name
		if seen[key] {
			diagnostics = append(diagnostics, duplicateDiagnostic(key))
		}
		seen[key] = true
	}
	for _, server := range pkg.MCPServers {
		key := "mcp:" + server.Name
		if seen[key] {
			diagnostics = append(diagnostics, duplicateDiagnostic(key))
		}
		seen[key] = true
		cfg := server.Config
		if len(cfg.EnvVars) > 0 || len(cfg.EnvHTTPHeaders) > 0 || cfg.BearerTokenEnvVar != "" {
			diagnostics = append(diagnostics, Diagnostic{Code: "validate/host_environment", Phase: "validate", Severity: SeverityError, Component: key, Message: "plugin MCP servers cannot forward the host environment", Remediation: "Use a declared {{secret:name}} or {{setting:name}} reference instead."})
		}
		for _, value := range cfg.Env {
			if strings.Contains(value, "$") {
				diagnostics = append(diagnostics, Diagnostic{Code: "validate/host_environment", Phase: "validate", Severity: SeverityError, Component: key, Message: "plugin MCP environment values cannot expand the host environment", Remediation: "Use a declared {{secret:name}} or {{setting:name}} reference instead."})
				break
			}
		}
		for header, value := range cfg.HTTPHeaders {
			lower := strings.ToLower(strings.TrimSpace(header))
			if (lower == "authorization" || lower == "proxy-authorization" || lower == "cookie" || lower == "x-api-key") && !strings.Contains(value, "{{secret:") {
				diagnostics = append(diagnostics, Diagnostic{Code: "validate/literal_secret", Phase: "validate", Severity: SeverityError, Component: key, Message: "sensitive plugin MCP headers must use a declared secret reference", Remediation: "Store the credential as a secret setting and reference it with {{secret:name}}."})
			}
		}
	}
	for _, setting := range pkg.Settings {
		key := "setting:" + setting.Name
		if seen[key] {
			diagnostics = append(diagnostics, duplicateDiagnostic(key))
		}
		seen[key] = true
		if !settingNameRE.MatchString(strings.TrimSpace(setting.Name)) {
			diagnostics = append(diagnostics, Diagnostic{Code: "validate/invalid_setting", Phase: "validate", Severity: SeverityError, Component: key, Message: "plugin setting has an invalid name", Remediation: "Use a short name containing letters, digits, dots, and hyphens."})
		}
		switch strings.ToLower(strings.TrimSpace(setting.Type)) {
		case "", "string", "number", "integer", "boolean", "object", "array":
		default:
			diagnostics = append(diagnostics, Diagnostic{Code: "validate/invalid_setting_type", Phase: "validate", Severity: SeverityError, Component: key, Message: "plugin setting has an unsupported type", Remediation: "Use string, number, integer, boolean, object, or array."})
		}
	}
	return diagnostics
}

func duplicateDiagnostic(component string) Diagnostic {
	return Diagnostic{Code: "validate/duplicate_component", Phase: "validate", Severity: SeverityError, Component: component, Message: "duplicate plugin component " + component, Remediation: "Give every component a unique authored name."}
}

func resolveResource(root, resource string) (string, string, error) {
	resource = strings.TrimSpace(resource)
	clean := filepath.Clean(resource)
	if resource == "" || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", "", pathEscapeError(resource, nil)
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", "", pathEscapeError(resource, err)
	}
	rootResolved, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", "", pathEscapeError(resource, err)
	}
	candidate := filepath.Join(rootResolved, clean)
	resolved := candidate
	if _, err := os.Lstat(candidate); err == nil {
		resolved, err = filepath.EvalSymlinks(candidate)
		if err != nil {
			return "", "", pathEscapeError(resource, err)
		}
	} else if !os.IsNotExist(err) {
		return "", "", pathEscapeError(resource, err)
	}
	rel, err := filepath.Rel(rootResolved, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", "", pathEscapeError(resource, err)
	}
	return filepath.ToSlash(filepath.Clean(clean)), resolved, nil
}

func pathEscapeError(resource string, cause error) error {
	return &Error{Diagnostic: Diagnostic{Code: "validate/path_escape", Phase: "validate", Severity: SeverityError, Component: resource, Message: fmt.Sprintf("plugin resource %q resolves outside the package root", resource), Remediation: "Use a package-relative path that does not traverse symlinks outside the plugin."}, Cause: cause}
}

func pluginError(phase, code, component, message, remediation string, cause error) error {
	return &Error{Diagnostic: Diagnostic{Code: code, Phase: phase, Severity: SeverityError, Component: component, Message: message, Remediation: remediation}, Cause: cause}
}
