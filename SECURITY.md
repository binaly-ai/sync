# Security

## Reporting a vulnerability

Please report security issues privately, not in a public issue: use GitHub's
[private vulnerability reporting](https://github.com/binaly-ai/sync/security/advisories/new)
for this repository, or email security@binaly.ai. We'll acknowledge within three working days.

## How releases are made

Releases are built by this repository's GitHub Actions release workflow
(`.github/workflows/release.yml`) from a tagged commit. Nothing is built or signed on a
developer's machine.

Each release publishes `release.json`, which names the version and the SHA-256 of each
executable, and `release.json.sig`, an ed25519 signature over it. Binaly Sync installs an
update only if the signature verifies against the public key in `internal/update/update.go`,
the version is newer than the one running, and the downloaded file's hash matches. The signing
key is a secret of this repository's GitHub Actions. `checksums.txt` lists the same hashes for
people checking a download by hand.

## Secrets

This repository contains no secrets. Binaly Sync signs in as a person, through their browser,
and keeps that sign-in in the operating system's credential store. Secret scanning with push
protection is enabled, and every pull request is scanned with gitleaks.
