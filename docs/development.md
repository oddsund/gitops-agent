# Development

## Tests

```
go test ./...
```

CI runs `gofmt -l .`, `go vet ./...`, and `go test ./...`, and it
cross-compiles for `linux/arm64` and `darwin/arm64`.

## The sops test fixture

`internal/sopsdecrypt/testdata` holds a single-use test key pair and a
file encrypted against it. Thus the tests run the real decryption path,
including the SSH-native age identity, instead of a mock.

That key protects nothing. Its own
[README](../internal/sopsdecrypt/testdata/README.md) has the detail.

## Conventions

[CLAUDE.md](../CLAUDE.md) has the conventions of this repository:
everything in English, conventional commits, comments that give a reason
rather than repeat the code, and shell scripts that show progress.
