# What Binaly Sync sends

A one-page summary for IT reviewers. Every claim here can be checked against the source code in
this repository.

## Where it connects

To the Binaly server it's signed in to, over HTTPS (port 443). The default is
`mcp.binaly.ai`. File uploads go to the same address; Binaly Sync never connects to a storage
provider directly.

To check for updates, it also downloads this repository's latest release information from
`github.com` (and GitHub's download hosts under `githubusercontent.com`) every few hours,
and the new version when there is one. Those requests send nothing about the lab, its files
or its users.

It opens no network ports. Its window is a Windows WebView2 window that talks to the program
directly and can't load anything from the internet. The one exception is signing in: while
you sign in (10 minutes at most), it listens on `127.0.0.1` (this computer only) so the browser can hand
back the sign-in, the standard method for desktop apps (RFC 8252).

## What it reads

Only the folders a person chose to sync, and only:

- **directory listings:** file names, sizes and modification times;
- **file contents, to compute a SHA-256 hash**, and only for files that are new or changed since
  the last run.

It skips Office lock files, temporary files, `Thumbs.db`, `desktop.ini`, and system and
version-control folders (`.git`, `$RECYCLE.BIN` and similar). It doesn't follow shortcuts,
symbolic links or junctions. It doesn't read files that are "online only" in OneDrive or Dropbox,
and it refuses Google Drive folders, because reading those would download them.

## What leaves the computer

For each file in a synced folder:

- its **path, size, modification time and SHA-256 hash**;
- for text data files (`.csv`, `.tsv`, `.txt`, `.dat`, `.asc`, `.xml`, `.xsl`, `.in`) and
  spreadsheets (`.xlsx`), **the first 2 KB of text** (for a spreadsheet, its first header
  cells). Binaly uses this to keep back files that look like they list people's personal
  details;
- **the file itself**, only when Binaly asks for it. Binaly asks for files up to 100 MB that
  aren't already stored. Larger files are only recorded by path and hash.

Each upload is checked by the server: it hashes what arrived and refuses anything that doesn't
match.

Once per check-in it also sends: a random install ID, the computer's name, the operating system,
Binaly Sync's version, and which drive letters map to which network shares (so paths appear the
way the lab knows them).

## What never leaves the computer

Files in folders nobody chose to sync. File contents beyond the hash, the 2 KB preview, and files
Binaly asked for. Passwords: sign-in happens in the person's own browser, on Binaly's page.

## Where sign-ins are kept

In Windows Credential Manager, under "Binaly Sync: ...", protected by the person's Windows login.
Binaly Sync holds no shared keys or secrets of its own. Disconnecting the computer in Binaly
revokes its access.

## Proof of what happened

Every run writes a log to `%LOCALAPPDATA%\Binaly\Sync\logs`: each file uploaded or registered,
the policy version the server applied, and every error. The last 30 runs are kept.

## Who decides what is uploaded

Binaly, not the program: the size limit, the file types previewed and the exclusions come from
the server at each check-in. That lets the rules change without a new release, and it means
this page describes the current defaults, not hard limits in the code.
