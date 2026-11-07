// Package selfupdate replaces the running gitops-agent binary with the
// newest GitHub release.
//
// Update checks the latest release tag, compares it with the version file,
// downloads the release asset into the binary's directory, verifies its
// sha256 and, when gh is available, its build provenance, atomically renames
// it over the binary, writes the version file and restarts the service.
//
// Trust: the checksum is served from the same origin as the binary, so it
// only guards against corruption. Build provenance verification is the real
// authenticity check and is skipped when gh is not installed. Private forks
// are not supported.
package selfupdate
