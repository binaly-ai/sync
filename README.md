# Binaly Sync

> **Pilot release.** Binaly Sync is being tried with its first labs. Releases aren't yet
> code-signed, so Windows shows "Windows protected your PC" the first time: choose More info,
> then Run anyway. Signed releases follow shortly.

Binaly Sync keeps your lab's folders connected to [Binaly](https://binaly.ai). It runs in the
Windows tray, notices new and changed files, and sends them to your lab's Binaly workspace,
where your AI assistant turns them into a searchable map of the lab's work.

It's a small, open-source program: you can read exactly what it does. It makes no decisions of
its own and contains no AI. For each file it reports a name, a size, a date and a fingerprint
(a SHA-256 hash), and Binaly answers: upload it, or just note that it exists.

- **What it sends, and what it never sends:** [WHAT-IT-SENDS.md](WHAT-IT-SENDS.md)
- **Reporting a security issue:** [SECURITY.md](SECURITY.md)

## Install (Windows)

Download `binaly-sync.exe` from the [latest release](https://github.com/binaly-ai/sync/releases/latest)
and run it (`binaly-sync-arm64.exe` for ARM computers). It installs itself for your Windows user
only (no administrator rights), adds itself to the Start menu, opens your browser to sign in to
Binaly, and asks which folders to sync. Before anything is sent, it shows you what it found.

It keeps itself up to date: it checks this repository's releases every few hours and installs a
new version when no sync is running. Settings can switch that to "tell me, don't install".

To remove it: Windows Settings, Apps, Installed apps, Binaly Sync, Uninstall. That removes the
program, its start-at-logon entry, its sign-ins and its local files. Files already sent to
Binaly stay there.

## Using it

The tray icon's menu has everything: Sync now, Pause, Add folder, Open in Binaly, Show log,
Workspaces and Settings. One installation can be signed in to several Binaly workspaces at once.

For IT staff and support, the same program works from a terminal:

```
binaly-sync preview <folder>      what would be synced; no sign-in, nothing leaves the computer
binaly-sync login                 sign in (opens your browser)
binaly-sync add <folder>          sync a folder from this computer
binaly-sync sync                  run one sync now
binaly-sync status                progress per workspace and folder
binaly-sync log                   where the latest run log is
binaly-sync update                install a newer release now
binaly-sync uninstall [--quiet]   remove it from this computer
```

## Building from source

Go 1.27 or later:

```
go generate ./cmd/binaly-sync     the icon and version information (optional)
go build -ldflags "-H windowsgui" -o binaly-sync.exe ./cmd/binaly-sync
go test ./...
```

A build from source reports its version as `dev` and never updates itself.

## License

Apache License 2.0. See [LICENSE](LICENSE).
