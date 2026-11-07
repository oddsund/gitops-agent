# Development

## Tests

```
go test ./...
```

CI runs `gofmt -l .`, `go vet ./...`, and `go test ./...`, and it
cross-compiles for `linux/arm64`, `linux/amd64` and `darwin/arm64`.

## The sops test fixture

`internal/sopsdecrypt/testdata` holds a single-use test key pair and a
file encrypted against it. Thus the tests run the real decryption path,
including the SSH-native age identity, instead of a mock.

That key protects nothing. Its own
[README](../internal/sopsdecrypt/testdata/README.md) has the detail.

## Releases

Pushing a tag that starts with `v` runs `.github/workflows/release.yml`. It has three stages:

1. `test` runs `go vet` and `go test`.
2. `build` runs once per architecture (`arm64`, `amd64`). Each run builds
   `gitops-agent-linux-<arch>` and its `.sha256` file, then attests the binary.
3. `release` downloads the files, checks them against their checksums and publishes them in one
   GitHub release. It is the only job with `contents: write`.

Do not rename the workflow or move the attest step into another workflow file. Installers and
`gitops-agent update` verify that `release.yml` signed the binary for the tag being installed.
A tag with a hyphen, such as `v1.7.0-rc1`, is published as a prerelease. `/releases/latest` skips it,
so no host installs or updates to it. To add an architecture, add it to the matrix in
`release.yml` and `ci.yml`, to the `case` in `scripts/install.sh` and to `assetNameFor` in
`internal/selfupdate/config.go`.

## Conventions

[CLAUDE.md](../CLAUDE.md) has the conventions of this repository:
everything in English, conventional commits, comments that give a reason
rather than repeat the code, and shell scripts that show progress.
