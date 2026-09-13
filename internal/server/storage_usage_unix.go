//go:build linux || darwin

package server

import (
	"io/fs"
	"syscall"
)

func storageAllocation(info fs.FileInfo) (storageFileID, int64, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return storageFileID{}, 0, false
	}
	return storageFileID{uint64(stat.Dev), uint64(stat.Ino)}, stat.Blocks * 512, true
}
