//go:build !darwin && !linux

package memory

// Platforms without a directory-relative, no-follow atomic writer fail closed.
// They can still run qualification and the pure placement/merge functions.
type fileTarget struct {
	rel    string
	before []byte
}

func openTarget(root, rel string) (*fileTarget, error) {
	return nil, errUnsupportedPlatform
}
func (*fileTarget) close()             {}
func (*fileTarget) check(string) error { return errUnsupportedPlatform }
func (*fileTarget) replace([]byte, func(string) error) error {
	return errUnsupportedPlatform
}
