package sopsdecrypt

import (
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"

	"github.com/oddsund/gitops-agent/internal/redact"
)

const (
	summaryIntegrity = "sops decrypt failed: integrity check (MAC) failed; see the journal"
	summaryRead      = "sops decrypt failed: cannot read secrets.enc.env; see the journal"
	summaryGeneric   = "sops decrypt failed; see the journal"

	detailIntegrity = "MAC mismatch (values not logged)"
	detailParse     = "dotenv parse error (offending line not logged)"

	detailUnclassified = "unclassified sops error"

	// dataKeyPrefix starts sops' getDataKeyError (sops v3.13.3,
	// usererrors.go).
	dataKeyPrefix = "Error getting data key:"
)

// maxDetail caps the journal detail at its last 2 KiB: the end of a message
// is usually the useful part.
const maxDetail = 2 << 10

// hexRun matches hex strings long enough to be a MAC or a digest. Age
// recipients and fingerprints are bech32, so key diagnostics survive. It
// also masks 40-character git SHAs and other long digests in key errors,
// which is acceptable.
var hexRun = regexp.MustCompile(`[0-9a-fA-F]{32,}`)

// classify maps an error from decrypt.File to a fixed summary and a detail
// for the journal. The sops text is matched only to pick one of the fixed
// strings, never copied into the summary, so a sops upgrade that rewords an
// error makes the summary less specific but cannot leak. The parse and read
// texts are pinned by tests against real sops; the integrity text (sops
// v3.13.3, decrypt/decrypt.go) is pinned only through the test seam because
// a real MAC mismatch is hard to build as a fixture.
//
// The integrity message quotes both MACs, which are unsalted SHA-512 over
// the plaintext values and so guessable offline for weak secrets. The parse
// message quotes a line of the file, which may be plaintext. Neither is
// logged.
func classify(err error, scrub *redact.Scrubber) (summary, detail string) {
	msg := err.Error()
	var pe *fs.PathError
	switch {
	case strings.HasPrefix(msg, "Failed to verify data integrity"):
		return summaryIntegrity, detailIntegrity
	case strings.Contains(msg, "invalid dotenv input line"):
		return summaryGeneric, detailParse
	case strings.HasPrefix(msg, "Failed to read ") && errors.As(err, &pe):
		return summaryRead, sanitize(msg, scrub)
	case strings.HasPrefix(msg, dataKeyPrefix):
		// Raised while recovering the data key, before any value is
		// decrypted, so nothing here derives from plaintext. The per-key
		// reasons (no matching identity, unreadable key file) are the
		// diagnostics an operator needs and only UserError carries them.
		var ue interface{ UserError() string }
		if errors.As(err, &ue) {
			msg = ue.UserError()
		}
		return summaryGeneric, sanitize(msg, scrub)
	}
	// Anything else may come from a stage that has seen plaintext: sops
	// quotes values in "Input string %s does not match sops' data format",
	// "Could not convert %s to bytes" and "Tree contains a non-string key".
	// A future message may quote more, so the text is not logged at all;
	// the Go type is the only hint.
	return summaryGeneric, fmt.Sprintf("%s (%T; error text not logged)", detailUnclassified, err)
}

// sanitize scrubs known secret values, masks long hex runs, and only then
// cuts, so the cut cannot split a value before it is scrubbed.
func sanitize(msg string, scrub *redact.Scrubber) string {
	msg = scrub.Scrub(msg)
	msg = hexRun.ReplaceAllString(msg, redact.Mask)
	if len(msg) > maxDetail {
		msg = "..." + msg[len(msg)-maxDetail:]
	}
	return msg
}

// wrapDecryptErr turns an error from decrypt.File into a *redact.Error.
// *redact.Error has no Unwrap, so errors.Is(err, fs.ErrNotExist) does not
// work on the result.
func wrapDecryptErr(err error, secretsBaseDir string) error {
	summary, detail := classify(err, redact.LoadEnvDir(secretsBaseDir))
	return &redact.Error{Summary: summary, Detail: detail}
}
