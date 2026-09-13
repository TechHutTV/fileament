//go:build !linux && !darwin

package server

import "io/fs"

func storageAllocation(fs.FileInfo) (storageFileID, int64, bool) {
	return storageFileID{}, 0, false
}
