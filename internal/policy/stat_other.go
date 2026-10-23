//go:build !unix

package policy

import "io/fs"

// inode is unavailable here; mtime and size alone identify a version.
func inode(fs.FileInfo) uint64 { return 0 }
