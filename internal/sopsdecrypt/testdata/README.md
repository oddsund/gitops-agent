Disposable fixture key, generated solely for these tests. It has never
protected anything real and never will — do not reuse it, and don't treat
its presence here as a leaked secret.

- `test_key` / `test_key.pub` — throwaway ed25519 SSH keypair
- `secrets.enc.env` — `decrypted.env` encrypted with sops against
  `test_key.pub` as the (SSH-native, unconverted) age recipient
- `decrypted.env` — the expected plaintext, for test assertions (named to
  avoid the repo's top-level `.gitignore` rule for `secrets.env`)
