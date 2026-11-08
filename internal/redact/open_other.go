//go:build !unix

package redact

import "os"

const openFlags = os.O_RDONLY
