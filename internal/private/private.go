// Package private checks that the relay's data directory and the credentials in it are readable
// by the relay's user alone, so a key restored with wider permissions is not left exposed.
package private

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Dir makes dir if it is missing and checks it is a folder only its owner can enter. A folder of
// the relay's own user with wider permissions is narrowed to 0700, and tightened says so;
// anything else is an error.
func Dir(dir string) (tightened bool, err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, fmt.Errorf("%s is not a folder", dir)
	}
	if !checked || info.Mode().Perm()&0o077 == 0 {
		return false, nil
	}
	if !ownedByMe(info) {
		return false, fmt.Errorf("%s can be read by other users and is not the relay user's to narrow; make it mode 0700", dir)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return false, err
	}
	return true, nil
}

// File checks a credential file, when it exists, is a plain file only its owner can read.
func File(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a plain file", path)
	}
	if checked && info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s can be read by other users; make it mode 0600 (chmod 600 %s)", path, path)
	}
	return nil
}

// Tree checks a folder of credentials and every file under it, as the ACME cache is.
func Tree(dir string) error {
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if _, err := Dir(dir); err != nil {
		return err
	}
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == dir {
			return nil
		}
		if d.IsDir() {
			_, err := Dir(path)
			return err
		}
		return File(path)
	})
}
