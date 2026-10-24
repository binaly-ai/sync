// Package ui serves Binaly Sync's screens - the preview before a folder is synced, the status
// page with the progress bar, and settings - as a local web page opened in the default
// browser. It listens on 127.0.0.1 only, every URL carries a random key, and the Host header
// must be this address, so neither another computer nor a web page (DNS rebinding) can reach it.
package ui

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"time"
)

//go:embed web
var web embed.FS

// Backend is what the pages can ask the app to do.
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
}

// Server is the running local page server.
type Server struct {
	Base string // e.g. http://127.0.0.1:53111/k/<key>/
	ln   net.Listener
}

// Start listens on a random local port.
func Start(b Backend) (*Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	kb := make([]byte, 16)
	rand.Read(kb)
	key := hex.EncodeToString(kb)
	host := ln.Addr().String()
	prefix := "/k/" + key + "/"
	static, _ := fs.Sub(web, "web")
	files := http.FileServer(http.FS(static))

	mux := http.NewServeMux()
	mux.HandleFunc(prefix, func(w http.ResponseWriter, r *http.Request) {
		if r.Host != host {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, prefix)
		if strings.HasPrefix(rest, "api/") {
			if r.Method == "POST" && r.Header.Get("x-binaly-sync") != "1" {
				// A cross-site form can't set this header, so a page elsewhere can't post here.
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			api(w, r, b, strings.TrimPrefix(rest, "api/"))
			return
		}
		w.Header().Set("cache-control", "no-store")
		w.Header().Set("content-security-policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:")
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + rest
		if rest == "" {
			r2.URL.Path = "/"
		}
		files.ServeHTTP(w, r2)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	go http.Serve(ln, mux)
	return &Server{Base: "http://" + host + prefix, ln: ln}, nil
}

func api(w http.ResponseWriter, r *http.Request, b Backend, name string) {
	var in struct {
		Folder     string          `json:"folder"`
		AccountKey string          `json:"accountKey"`
		Minutes    int             `json:"minutes"`
		Settings   json.RawMessage `json:"settings"`
	}
	if r.Method == "POST" {
		json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in)
	}
	var out any
	var err error
	switch name {
	case "status":
		out = b.Status()
	case "settings":
		if r.Method == "POST" {
			err = b.SaveSettings(in.Settings)
		}
		out = b.Settings()
	case "preview":
		out, err = b.Preview(in.Folder)
	case "pick":
		var p string
		p, err = b.PickFolder()
		out = map[string]string{"folder": p}
	case "start":
		err = b.Start(in.Folder, in.AccountKey)
		out = map[string]bool{"ok": err == nil}
	case "sync":
		b.SyncNow()
		out = map[string]bool{"ok": true}
	case "pause":
		if in.Minutes > 0 {
			b.Pause(time.Duration(in.Minutes) * time.Minute)
		} else {
			b.Resume()
		}
		out = map[string]bool{"ok": true}
	case "signin":
		err = b.SignIn()
		out = map[string]bool{"ok": err == nil}
	case "signout":
		err = b.SignOut(in.AccountKey)
		out = map[string]bool{"ok": err == nil}
	case "confirm-deletions":
		b.ConfirmDeletions(in.AccountKey)
		out = map[string]bool{"ok": true}
	case "update":
		b.InstallUpdate()
		out = map[string]bool{"ok": true}
	case "log":
		b.OpenLog()
		out = map[string]bool{"ok": true}
	default:
		http.NotFound(w, r)
		return
	}
	w.Header().Set("content-type", "application/json")
	w.Header().Set("cache-control", "no-store")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(out)
}

// URL returns the address of a screen ("status", "settings", "preview?folder=...").
func (s *Server) URL(screen string) string {
	return fmt.Sprintf("%s#%s", s.Base, screen)
}
