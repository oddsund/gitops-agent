//go:build !unix

package notify

import "os"

// ownerUID is unavailable off Unix; the owner check is skipped there.
func ownerUID(os.FileInfo) (int, bool) { return 0, false }
