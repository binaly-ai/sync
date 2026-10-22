# Security

## Reporting a vulnerability

Please report security issues privately, not in a public issue: use GitHub's
[private vulnerability reporting](https://github.com/binaly-ai/sync/security/advisories/new)
for this repository, or email security@binaly.ai. We'll acknowledge within three working days.

## How releases are made

Releases are built by this repository's GitHub Actions release workflow from a tagged commit.
Each release publishes `checksums.txt` and its signature; Binaly Sync verifies that signature
before installing an update. Nothing is built or signed on a developer's machine.

## Secrets

This repository contains no secrets. Binaly Sync signs in as a person, through their browser,
and keeps that sign-in in the operating system's credential store. Secret scanning with push
protection is enabled, and every pull request is scanned with gitleaks.
