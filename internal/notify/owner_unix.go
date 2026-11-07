//go:build unix

package notify

import (
	"os"
	"syscall"
)

// ownerUID reports the uid that owns the open file.
func ownerUID(fi os.FileInfo) (int, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}
