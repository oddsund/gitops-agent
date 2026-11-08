//go:build unix

package redact

import "syscall"

// openFlags make the open refuse a symlink and never block on a FIFO that
// replaced the file after the directory was listed.
const openFlags = syscall.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
