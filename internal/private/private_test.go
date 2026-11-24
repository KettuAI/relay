//go:build unix

package private

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirAndFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if tightened, err := Dir(dir); err != nil || tightened {
		t.Fatalf("new folder: %v %v", tightened, err)
	}
	os.Chmod(dir, 0o755)
	if tightened, err := Dir(dir); err != nil || !tightened {
		t.Fatalf("wide folder: %v %v", tightened, err)
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
		t.Fatalf("folder mode %v", info.Mode().Perm())
	}

	key := filepath.Join(dir, "access-key")
	if err := File(key); err != nil {
		t.Fatal("a missing file is refused")
	}
	os.WriteFile(key, []byte("x"), 0o644)
	if err := File(key); err == nil {
		t.Fatal("a readable key is taken")
	}
	os.Chmod(key, 0o600)
	if err := File(key); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	os.Symlink(key, link)
	if err := File(link); err == nil {
		t.Fatal("a link is taken")
	}

	acme := filepath.Join(dir, "acme")
	os.MkdirAll(acme, 0o700)
	os.WriteFile(filepath.Join(acme, "acme_account+key"), []byte("x"), 0o640)
	if err := Tree(acme); err == nil {
		t.Fatal("a readable ACME key is taken")
	}
}
