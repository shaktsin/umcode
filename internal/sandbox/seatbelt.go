package sandbox

import (
	"fmt"
	"os/exec"
	"strings"
)

// seatbelt runs commands under macOS's sandbox-exec.
type seatbelt struct{ exe string }

func (seatbelt) Name() string { return "seatbelt" }

func (s seatbelt) Wrap(cmd *exec.Cmd, p Policy) error {
	profile, err := seatbeltProfile(p)
	if err != nil {
		return err
	}
	inner := append([]string{cmd.Path}, cmd.Args[1:]...)
	cmd.Path = s.exe
	cmd.Args = append([]string{s.exe, "-p", profile, "--"}, inner...)
	return nil
}

// seatbeltProfile builds an SBPL profile. It starts from allow-default and
// then denies what must be confined; in Seatbelt the last matching rule wins,
// so the narrow allows come after the broad deny.
func seatbeltProfile(p Policy) (string, error) {
	var b strings.Builder
	b.WriteString("(version 1)\n(allow default)\n")
	if !p.Network {
		b.WriteString("(deny network*)\n")
	}
	b.WriteString("(deny file-write*)\n")
	writable := append([]string{p.Root}, p.Writable...)
	b.WriteString("(allow file-write*\n")
	for _, w := range writable {
		q, err := sbplQuote(w)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "  (subpath %s)\n", q)
	}
	for _, dev := range []string{"/dev/null", "/dev/zero", "/dev/tty", "/dev/dtracehelper"} {
		fmt.Fprintf(&b, "  (literal %q)\n", dev)
	}
	b.WriteString("  (regex #\"^/dev/fd/[0-9]+$\")\n  (regex #\"^/dev/ttys[0-9]+$\")\n)\n")
	for _, ro := range p.ReadOnlyInRoot {
		q, err := sbplQuote(ro)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "(deny file-write* (subpath %s))\n", q)
	}
	for _, d := range p.DenyRead {
		q, err := sbplQuote(d)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "(deny file-read* (subpath %s))\n", q)
	}
	return b.String(), nil
}

// sbplQuote returns path as an SBPL string literal.
func sbplQuote(path string) (string, error) {
	if path == "" || strings.ContainsAny(path, "\x00\n\r") {
		return "", fmt.Errorf("sandbox: unsupported path %q", path)
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(path) + `"`, nil
}
