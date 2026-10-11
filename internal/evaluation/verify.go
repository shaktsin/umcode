package evaluation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/shaktsin/umcode/internal/procutil"
	"github.com/shaktsin/umcode/internal/vault"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type boundedOutput struct{ b bytes.Buffer }

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := (64 << 10) - b.b.Len()
	if remaining > 0 {
		b.b.Write(p[:min(len(p), remaining)])
	}
	return n, nil
}
func zero() *int { v := 0; return &v }
func Verify(ctx context.Context, t Task, artifactDir, verifierRoot string) ([]CheckResult, error) {
	if t.TrustedRoot == "" || t.TrustedHash == "" {
		return nil, errors.New("trusted verifier prerequisites are required")
	}
	before, err := TreeHash(t.TrustedRoot)
	if err != nil {
		return nil, err
	}
	if before != t.TrustedHash {
		return nil, errors.New("trusted verifier hash mismatch")
	}
	if err := CopyTree(t.TrustedRoot, verifierRoot); err != nil {
		return nil, err
	}
	for _, rel := range t.Artifacts {
		src, err := SafeJoin(artifactDir, rel)
		if err != nil {
			return nil, err
		}
		info, err := os.Lstat(src)
		if err != nil || !info.Mode().IsRegular() || info.Size() > treeLimit {
			return nil, errors.New("invalid declared artifact")
		}
		dest := filepath.Join(verifierRoot, rel)
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			return nil, err
		}
		in, err := os.Open(src)
		if err != nil {
			return nil, err
		}
		out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			in.Close()
			return nil, errors.New("artifact collides with trusted verifier")
		}
		_, err = io.Copy(out, io.LimitReader(in, treeLimit+1))
		in.Close()
		closeErr := out.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	var results []CheckResult
	for _, check := range t.Checks {
		if len(check.Argv) == 0 || check.Seconds <= 0 {
			return nil, errors.New("invalid verifier command")
		}
		cctx, cancel := context.WithTimeout(ctx, time.Duration(check.Seconds)*time.Second)
		cmd := exec.CommandContext(cctx, check.Argv[0], check.Argv[1:]...)
		cmd.Dir = verifierRoot
		cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/local/bin:/opt/homebrew/bin", "HOME=" + verifierRoot, "TMPDIR=" + verifierRoot, "GOCACHE=" + filepath.Join(verifierRoot, ".cache"), "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "GOWORK=off"}
		procutil.Prepare(cmd)
		var output boundedOutput
		cmd.Stdout = &output
		cmd.Stderr = &output
		err := cmd.Run()
		status := "passed"
		if err != nil {
			status = "failed"
			var exit *exec.ExitError
			if !errors.As(err, &exit) || cctx.Err() != nil {
				status = "incomplete"
			}
		}
		if cctx.Err() != nil {
			fmt.Fprintf(&output, "\n%s", cctx.Err())
		}
		cancel()
		body, _ := vault.Redact(output.b.Bytes())
		h := sha256.Sum256(body)
		r := CheckResult{ID: check.ID, Status: status, EvidenceHash: hex.EncodeToString(h[:]), Output: string(body), Safety: check.Safety}
		if status != "incomplete" {
			r.SafetyViolations = zero()
			r.UnsupportedClaims = zero()
			r.ContextOmissions = zero()
			r.StaleAuthority = zero()
			if check.Safety && status == "failed" {
				v := 1
				r.SafetyViolations = &v
			}
		}
		results = append(results, r)
	}
	after, err := TreeHash(t.TrustedRoot)
	if err != nil || after != before {
		return nil, errors.New("trusted prerequisites changed during verification")
	}
	return results, nil
}
