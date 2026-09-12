package server

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const safetyBackupKeep = 3

func pruneSafetyBackups(dir, newest string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if name == newest || !isSafetyBackupName(name) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			names = append(names, name)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	for index, name := range names {
		if index >= safetyBackupKeep-1 {
			if err := os.Remove(filepath.Join(dir, name)); err != nil {
				return err
			}
		}
	}
	return syncDirectory(dir)
}

func isSafetyBackupName(name string) bool {
	name, ok := strings.CutPrefix(name, "pre-restore-")
	if !ok {
		return false
	}
	name, ok = strings.CutSuffix(name, ".fileament")
	if !ok {
		return false
	}
	stamp, id, ok := strings.Cut(name, "-")
	if !ok || len(id) != 26 || !validStorageID(id) {
		return false
	}
	_, err := time.Parse("20060102T150405Z", stamp)
	return err == nil
}
