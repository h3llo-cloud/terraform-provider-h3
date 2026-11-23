# Release signing keys

Public keys only. The same files are in h3terraform and h3cli and are published on `https://dl.h3llo.cloud/keys/`.

```
owner key   98E8 9F85 3412 C315 1E0B 14C9 4F28 F12C 8BE9 F40C   ed25519, OpenPGP applet of the owner's YubiKey,
            (owner-<fpr>.asc)                                    offline; only certifies release keys, never in CI
   │ certification (notation role@h3llo.cloud=release-ci)
   ▼
release key RSA-4096, sign, 1 year                              CI only (protected variable / OpenBao),
            (release/h3-release-<date>-<keyid>.asc)              signs SHA256SUMS of every release
```

- RSA-4096 because the Terraform Registry accepts RSA/DSA keys only ("not the default ECC type").
- `scripts/gpg-setup.sh` (release job) refuses to sign unless the CI key is RSA ≥ 4096, valid for 7+ days, committed
  in `release/` and carries a valid, not revoked certification by the owner key pinned in the script.
- `scripts/verify-release.sh SHA256SUMS SHA256SUMS.sig` checks a release against this chain (users, smoke jobs).
  An expired release key is still accepted for old releases (Terraform does the same); a revoked one is not.
- New key / yearly rotation: owner runs `security/release-signing/release-key-ceremony-2026-11.sh` offline, the public
  `.asc` comes here by MR, the old file stays (old releases), the new key is added in the Terraform Registry
  (Settings → Signing Keys) and in `opentofu/registry`. Runbook: platform/release-signing-tf-cli-2026-11.md (workspace).
