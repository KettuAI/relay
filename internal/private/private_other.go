//go:build !unix

package private

import "io/fs"

// Windows keeps no Unix permission bits; its folders' access lists are left to the system.
const checked = false

func ownedByMe(fs.FileInfo) bool { return false }
