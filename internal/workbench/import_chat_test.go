package workbench

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestImportedChatRejectsUnlistedRuntimeFiles(t *testing.T) {
	for _, checkpoint := range []bool{false, true} {
		t.Run(map[bool]string{false: "original", true: "checkpoint"}[checkpoint], func(t *testing.T) {
			project, id, created, manifest, assets := runtimeBundleFixture(t)
			b := BundleTarget{project, id, created}
			upload, err := b.Begin(manifest)
			if err != nil {
				t.Fatal(err)
			}
			for name, content := range assets {
				if err = b.Chunk(upload, name, 0, base64.StdEncoding.EncodeToString(content)); err != nil {
					t.Fatal(err)
				}
			}
			if err = b.Finish(upload); err != nil {
				t.Fatal(err)
			}
			verify := b.VerifiedPath
			if checkpoint {
				out := t.TempDir()
				for name, content := range assets {
					if err = os.WriteFile(filepath.Join(out, name), content, 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err = publishImportedCheckpoint(b, out, "checkpoint-test"); err != nil {
					t.Fatal(err)
				}
				verify = func() (string, error) { return verifiedImportedCheckpoint(b, "checkpoint-test") }
			}
			path, err := verify()
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(path, "chat_template.jinja"), []byte("unverified override"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err = verify(); err == nil {
				t.Fatal("accepted an unmanifested chat-template override")
			}
		})
	}
}
