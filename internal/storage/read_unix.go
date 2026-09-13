//go:build unix

package storage

import "syscall"

const nonblockReadFlag = syscall.O_NONBLOCK
