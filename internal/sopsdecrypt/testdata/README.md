Engangs-testnøkkel, generert utelukkende for disse testene. Den har aldri
beskyttet noe ekte og kommer aldri til å gjøre det — ikke gjenbruk den, og
ikke behandle det at den ligger her som en lekket hemmelighet.

- `test_key` / `test_key.pub` — engangs ed25519 SSH-nøkkelpar
- `secrets.enc.env` — `decrypted.env` kryptert med sops mot `test_key.pub`
  som (SSH-native, ukonvertert) age-mottaker
- `decrypted.env` — forventet klartekst, for test-assertions (navngitt for
  å unngå repoets `.gitignore`-regel for `secrets.env`)
