// Package ui is Binaly Sync's window: the preview before a folder is synced, the status page
// with the progress bar, and settings. The screens are one HTML document shown in a WebView2
// window (the component Windows itself ships, also used by Teams and Outlook). The page talks
// to the program through the window directly: nothing listens on a network port, and the page
// can't load anything from the internet (its content security policy forbids it).
package ui

import (
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

//go:embed web
var web embed.FS

// Backend is what the screens can ask the app to do.
type Backend interface {
	Status() any
	Preview(folder string) (any, error)
	Start(folder, accountKey string) error
	SyncNow()
	Pause(d time.Duration)
	Resume()
	Settings() any
	SaveSettings(raw json.RawMessage) error
	SignIn() error
	SignOut(accountKey string) error
	PickFolder() (string, error)
	ConfirmDeletions(accountKey string)
	OpenLog()
	InstallUpdate()
	CheckUpdate()
	OpenSetup(accountKey string)
	OpenInBinaly(accountKey string)
	RemoveFolder(accountKey, folderID string) error
	// OpenURL opens an address in the person's browser.
	OpenURL(url string)
	// Assistant returns a workspace's "Connect your AI assistant" steps, from its server.
	Assistant(accountKey string) (any, error)
	// OpenDocs opens the workspace's guide to connecting an AI assistant, at the address its
	// server gave (checked to be Binaly's own).
	OpenDocs(accountKey string)
}

// Links are the only addresses the page can open: a name, never a URL from the page, so the
// page can't send the browser anywhere else. (A plain link would navigate this window itself.)
var Links = map[string]string{
	"website": "https://binaly.ai",
	"source":  "https://github.com/binaly-ai/sync",
	"sends":   "https://github.com/binaly-ai/sync/blob/main/WHAT-IT-SENDS.md",
}

var ok = map[string]bool{"ok": true}

// Call runs one request from the page.
func Call(b Backend, name string, raw []byte) (any, error) {
	var in struct {
		Folder     string          `json:"folder"`
		AccountKey string          `json:"accountKey"`
		Minutes    int             `json:"minutes"`
		Link       string          `json:"link"`
		FolderID   string          `json:"folderId"`
		Settings   json.RawMessage `json:"settings"`
	}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
	}
	switch name {
	case "status":
		return b.Status(), nil
	case "settings":
		if len(in.Settings) > 0 {
			if err := b.SaveSettings(in.Settings); err != nil {
				return nil, err
			}
		}
		return b.Settings(), nil
	case "preview":
		return b.Preview(in.Folder)
	case "pick":
		p, err := b.PickFolder()
		return map[string]string{"folder": p}, err
	case "start":
		return ok, b.Start(in.Folder, in.AccountKey)
	case "sync":
		b.SyncNow()
	case "pause":
		// minutes > 0: pause that long; -1: until resumed; 0: resume.
		switch {
		case in.Minutes > 0:
			b.Pause(time.Duration(in.Minutes) * time.Minute)
		case in.Minutes < 0:
			b.Pause(0)
		default:
			b.Resume()
		}
	case "signin":
		return ok, b.SignIn()
	case "signout":
		return ok, b.SignOut(in.AccountKey)
	case "confirm-deletions":
		b.ConfirmDeletions(in.AccountKey)
	case "log":
		b.OpenLog()
	case "update":
		b.InstallUpdate()
	case "check-update":
		b.CheckUpdate()
	case "open-setup":
		b.OpenSetup(in.AccountKey)
	case "remove-folder":
		return ok, b.RemoveFolder(in.AccountKey, in.FolderID)
	case "open-link":
		u, known := Links[in.Link]
		if !known {
			return nil, errors.New("unknown link " + in.Link)
		}
		b.OpenURL(u)
	case "open-binaly":
		b.OpenInBinaly(in.AccountKey)
	case "assistant":
		return b.Assistant(in.AccountKey)
	case "open-docs":
		b.OpenDocs(in.AccountKey)
	default:
		return nil, errors.New("unknown request " + name)
	}
	return ok, nil
}

// Page is the whole UI as one document, with its stylesheet, script and icon inlined.
func Page() string {
	read := func(name string) string {
		b, err := web.ReadFile("web/" + name)
		if err != nil {
			panic(err) // embedded at build time
		}
		return string(b)
	}
	icon := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte(read("icon.png")))
	return strings.NewReplacer(
		`<link rel="stylesheet" href="style.css">`, "<style>\n"+read("style.css")+"</style>",
		`<script src="app.js"></script>`, "<script>\n"+read("app.js")+"</script>",
		`src="icon.png"`, `src="`+icon+`"`,
	).Replace(read("index.html"))
}
