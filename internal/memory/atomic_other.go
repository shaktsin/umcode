//go:build !darwin && !linux

package memory

import "errors"

// Platforms without a directory-relative, no-follow atomic writer fail closed.
// They can still run qualification and the pure placement/merge functions.
type fileTarget struct {
	rel    string
	before []byte
}

func openTarget(root, rel string) (*fileTarget, error) {
	return nil, errors.New("contained memory writes unavailable")
}
func (*fileTarget) close()             {}
func (*fileTarget) check(string) error { return errors.New("contained memory writes unavailable") }
func (*fileTarget) replace([]byte, func(string) error) error {
	return errors.New("contained memory writes unavailable")
}
