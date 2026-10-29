# Binaly Sync

> **Pilot release.** Binaly Sync is being tried with its first labs. Releases aren't yet
> code-signed, so Windows shows "Windows protected your PC" the first time: choose More info,
> then Run anyway. Signed releases follow shortly.

Binaly Sync keeps your lab's folders connected to [Binaly](https://binaly.ai). It runs in the
Windows tray, notices new and changed files, and sends them to your lab's Binaly workspace.
There, a scheduled task in the Claude Desktop app, set up once from the lab's Binaly Sync page
(app.binaly.ai/sync), turns them into a searchable map of the lab's work.

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

After that it runs in the background. Windows tells it when something changes in a synced
folder, and it syncs that folder about half a minute after the changes stop. It also looks
through every folder in full every 15 minutes, in case a change notice was missed (network drives
drop them). Settings, "Check folders every", changes that interval on this computer.

OneNote notebooks: where the OneNote desktop app is installed, each new or changed section is
exported to PDF by OneNote itself and sent in place of the `.one` file, which nothing else
reads well, together with each page's title, dates, authors and text (including the words
OneNote recognized in handwriting and pictures). Without the app, Binaly Sync says how many
sections it can't include.

Text and dates: with each file it sends the file's creation time and what the document says
about itself (title, author, dates, DOI, page count). When Binaly asks, it also sends a file's
typed text page by page (PDF, Word, PowerPoint, Excel and plain text), so the lab's AI assistant
can search and read whole documents. [WHAT-IT-SENDS.md](WHAT-IT-SENDS.md) has the details.

It keeps itself up to date: it checks this repository's releases every few hours and installs a
new version when one appears, stopping a running sync first (the next sync picks up where it
left off). Settings can switch that to "tell me, don't install".

To remove it: Windows Settings, Apps, Installed apps, Binaly Sync, Uninstall. That removes the
program, its start-at-logon entry, its sign-ins and its local files. Files already sent to
Binaly stay there.

## Using it

Click the tray icon to open Binaly Sync's window:

- **Status:** each workspace's progress (processed, waiting for the LLM, waiting to upload,
  needs attention), the folders being synced, Add folder, Stop syncing a folder, Sync now,
  Pause (for 1, 6 or 24 hours, or until you resume) and Open in Binaly.
- **Tree:** this computer's folders, synced or not, with how many of each synced folder's files
  are processed, waiting, held or left out, and each file's state and notes. Mark a synced
  folder **Process first** (its files are processed before everything else) or **Leave out**
  (its files aren't processed, and no computer sends them any more), put held files back with
  **Read it**, or add a folder that isn't synced yet.
- **AI assistant:** how to connect your AI assistant (Claude, ChatGPT and others) to the
  workspace, and the prompt that sets up hourly processing in Claude Desktop, with copy buttons.
- **Settings:** start at Windows sign-in, how updates install, an upload speed limit, and the
  workspaces this computer is signed in to (Add workspace).
- **About:** the version, Check for updates, the run logs, and links to this page and the source.

The tray menu itself only opens the window (or its AI assistant page), shows the version,
checks for updates and quits.
One installation can be signed in to several Binaly workspaces at once.

For IT staff and support, the same program works from a terminal:

```
binaly-sync preview <folder>      what would be synced; no sign-in, nothing leaves the computer
binaly-sync login                 sign in (opens your browser)
binaly-sync logout                sign out of a workspace
binaly-sync workspaces            the workspaces this computer is signed in to
binaly-sync add <folder>          sync a folder from this computer
binaly-sync remove <folder>       stop syncing a folder from this computer
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
