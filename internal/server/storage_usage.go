package server

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type storageUsage struct {
	TotalBytes     int64  `json:"totalBytes"`
	LibraryBytes   int64  `json:"libraryBytes"`
	ThumbnailBytes int64  `json:"thumbnailBytes"`
	DatabaseBytes  int64  `json:"databaseBytes"`
	BackupBytes    int64  `json:"backupBytes"`
	WorkspaceBytes int64  `json:"workspaceBytes"`
	OtherBytes     int64  `json:"otherBytes"`
	DiskBytes      *int64 `json:"diskBytes"`
}

type storageFileID struct{ device, inode uint64 }

func (usage *storageUsage) measure(ctx context.Context, root string) error {
	var diskBytes int64
	allocatedAvailable := true
	seen := make(map[storageFileID]struct{})
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := d.Info()
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		id, allocated, ok := storageAllocation(info)
		if !ok {
			allocatedAvailable = false
		} else if _, exists := seen[id]; !exists {
			seen[id] = struct{}{}
			diskBytes += allocated
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		size := info.Size()
		switch parts[0] {
		case "models":
			if len(parts) > 2 && parts[2] == "thumbs" {
				usage.ThumbnailBytes += size
			} else {
				usage.LibraryBytes += size
			}
		case "collections.json":
			usage.LibraryBytes += size
		case "fileament.db", "fileament.db-wal", "fileament.db-shm", "fileament.db-journal":
			usage.DatabaseBytes += size
		case "backups":
			usage.BackupBytes += size
		case "tmp", ".restore", ".mutations":
			usage.WorkspaceBytes += size
		default:
			usage.OtherBytes += size
		}
		return nil
	})
	if err == nil && allocatedAvailable {
		usage.DiskBytes = &diskBytes
	}
	return err
}
