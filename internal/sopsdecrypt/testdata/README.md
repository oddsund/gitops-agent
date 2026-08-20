A throwaway, single-use test key, generated solely for these tests. It has
never protected anything real and never will -- don't reuse it, and don't
treat its presence here as a leaked secret.

- `test_key` / `test_key.pub` -- single-use ed25519 SSH key pair
- `secrets.enc.env` -- `decrypted.env` encrypted with sops against
  `test_key.pub` as an (SSH-native, unconverted) age recipient
- `decrypted.env` -- the expected plaintext, for test assertions (named to
  avoid the repo's `.gitignore` rule for `secrets.env`)
