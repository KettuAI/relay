//go:build unix

package private

import (
	"io/fs"
	"os"
	"syscall"
)

const checked = true

func ownedByMe(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
