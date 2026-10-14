package evaluation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const treeLimit = int64(64 << 20)
const ownerFile = ".evaluation-owner.json"

type owner struct {
	Campaign     string `json:"campaign"`
	Unit         string `json:"unit"`
	ManifestHash string `json:"manifest_hash"`
}

func ordinaryRoot(root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("evaluation root is a non-directory/symlink")
	}
	return nil
}
func SafeJoin(base, rel string) (string, error) {
	if !SafePath(rel) {
		return "", errors.New("unsafe relative path")
	}
	base, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", err
	}
	p := base
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		p = filepath.Join(p, part)
		info, err := os.Lstat(p)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("symlink in fixture path")
		}
	}
	return p, nil
}

func TreeHash(root string) (string, error) {
	if err := ordinaryRoot(root); err != nil {
		return "", err
	}
	h := sha256.New()
	var total int64
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			return nil
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() {
			return errors.New("fixture contains nonregular entry")
		}
		total += info.Size()
		if total > treeLimit {
			return errors.New("fixture exceeds 64 MiB")
		}
		rel, _ := filepath.Rel(root, path)
		fmt.Fprintf(h, "%d:%s:%d:", len(rel), filepath.ToSlash(rel), info.Size())
		f, e := os.Open(path)
		if e != nil {
			return e
		}
		_, e = io.Copy(h, io.LimitReader(f, info.Size()+1))
		f.Close()
		return e
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func Stage(root string, m Manifest, u Unit) (string, error) {
	if e := Validate(m); e != nil {
		return "", e
	}
	if !safeID.MatchString(u.ID) {
		return "", errors.New("unsafe unit ID")
	}
	t, ok := m.Task(u.TaskID)
	if !ok {
		return "", errors.New("unknown task")
	}
	if err := ordinaryRoot(root); err != nil {
		return "", err
	}
	src, err := SafeJoin(m.BaseDir, t.Fixture)
	if err != nil {
		return "", err
	}
	hash, err := TreeHash(src)
	if err != nil {
		return "", err
	}
	if hash != t.FixtureHash {
		return "", errors.New("fixture hash mismatch")
	}
	path := filepath.Join(root, u.ID)
	if err := os.Mkdir(path, 0700); err != nil {
		return "", err
	}
	o := owner{m.ID, u.ID, Hash(m)}
	b, _ := json.Marshal(o)
	if err := os.WriteFile(filepath.Join(path, ownerFile), b, 0600); err != nil {
		return "", err
	}
	dest := filepath.Join(path, "workspace")
	if err := CopyTree(src, dest); err != nil {
		return path, err
	}
	return path, nil
}
func CopyTree(src, dest string) error {
	if _, err := TreeHash(src); err != nil {
		return err
	}
	if err := os.Mkdir(dest, 0700); err != nil {
		return err
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		if rel == "." {
			return nil
		}
		target := filepath.Join(dest, rel)
		if d.IsDir() {
			return os.Mkdir(target, 0700)
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("nonregular copy source")
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600|info.Mode().Perm()&0100)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, io.LimitReader(in, treeLimit+1))
		closeErr := out.Close()
		if err != nil {
			return err
		}
		return closeErr
	})
}
func CleanupOwned(path, campaignID string) error {
	if err := ordinaryRoot(path); err != nil {
		return err
	}
	b, err := os.ReadFile(filepath.Join(path, ownerFile))
	if err != nil {
		return err
	}
	var o owner
	if err := json.Unmarshal(b, &o); err != nil {
		return err
	}
	if o.Campaign != campaignID || o.Unit == "" || o.ManifestHash == "" || !strings.EqualFold(filepath.Base(path), o.Unit) {
		return errors.New("evaluation ownership mismatch")
	}
	return os.RemoveAll(path)
}
