//go:build darwin

package secrets

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// keychain stores secrets as generic passwords via /usr/bin/security.
// Secrets are written through `security -i` on stdin so they never appear in
// the process list.
type keychain struct{}

func platformStore() Store { return keychain{} }

func (keychain) Backend() string { return "keychain" }

func (keychain) Get(key string) (string, error) {
	out, err := exec.Command("/usr/bin/security", "find-generic-password",
		"-s", Service, "-a", key, "-w").Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 44 {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("keychain read: %w", err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}

func (keychain) Set(key, value string) error {
	if strings.ContainsAny(value, "\"\\\n\r") || strings.ContainsAny(key, "\"\\\n\r") {
		return errors.New("keychain: secret contains unsupported characters")
	}
	cmd := exec.Command("/usr/bin/security", "-i")
	cmd.Stdin = strings.NewReader(fmt.Sprintf(
		"add-generic-password -U -s \"%s\" -a \"%s\" -l \"UMCode: %s\" -w \"%s\"\n",
		Service, key, key, value))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("keychain write: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (keychain) Delete(key string) error {
	err := exec.Command("/usr/bin/security", "delete-generic-password",
		"-s", Service, "-a", key).Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 44 {
		return nil
	}
	return err
}
