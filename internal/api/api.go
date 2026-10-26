// Package api is the client for the Binaly Sync API (/sync/v1 on the Binaly server). The
// server makes every decision; this package only asks and passes the answers on.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Tokens supplies access tokens; auth.Source implements it.
type Tokens interface {
	Token(ctx context.Context) (string, error)
	Invalidate()
}

// Client talks to one server as one account.
type Client struct {
	Server string
	Tokens Tokens
	HTTP   *http.Client
}

// Error is a refusal from the server, with its reason.
type Error struct {
	Status int
	Code   string `json:"error"`
	Detail string `json:"detail"`
}

func (e *Error) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Detail)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Code)
}

// Is lets errors.Is match on the server's error code.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code
}

// Errors the caller acts on.
var (
	ErrRevoked    = &Error{Code: "revoked"}
	ErrNotSyncing = &Error{Code: "not-syncing"}
)

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 5 * time.Minute}
}

// gatewayRetryWait is how long a call waits before its one retry after a 502, 503 or 504.
var gatewayRetryWait = 2 * time.Second

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	retriedGateway := false
	for attempt := 0; attempt < 2; attempt++ {
		tok, err := c.Tokens.Token(ctx)
		if err != nil {
			return err
		}
		var body io.Reader
		if in != nil {
			b, err := json.Marshal(in)
			if err != nil {
				return err
			}
			body = bytes.NewReader(b)
		}
		req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.Server, "/")+path, body)
		if err != nil {
			return err
		}
		req.Header.Set("authorization", "Bearer "+tok)
		req.Header.Set("content-type", "application/json")
		res, err := c.http().Do(req)
		if err != nil {
			return err
		}
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<20))
		res.Body.Close()
		if res.StatusCode == http.StatusUnauthorized && attempt == 0 {
			// The access token expired early or was revoked; refresh once and retry.
			c.Tokens.Invalidate()
			continue
		}
		// A proxy's 502/503/504 (a dropped connection, a restart) is retried once. Every call
		// is safe to repeat: a report of the same files in the same scan says the same thing.
		if (res.StatusCode == http.StatusBadGateway || res.StatusCode == http.StatusServiceUnavailable || res.StatusCode == http.StatusGatewayTimeout) && !retriedGateway {
			retriedGateway = true
			attempt--
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(gatewayRetryWait):
			}
			continue
		}
		if res.StatusCode != 200 {
			e := &Error{Status: res.StatusCode}
			_ = json.Unmarshal(raw, e)
			if e.Code == "" {
				e.Code = strings.TrimSpace(string(raw))
			}
			return e
		}
		if out != nil {
			return json.Unmarshal(raw, out)
		}
		return nil
	}
	return errors.New("unauthorized")
}

// Policy is the server's rules for this machine.
type Policy struct {
	Version             string   `json:"version"`
	UploadLimitBytes    int64    `json:"uploadLimitBytes"`
	ExcludeNames        []string `json:"excludeNames"`
	ExcludeDirs         []string `json:"excludeDirs"`
	PreviewExtensions   []string `json:"previewExtensions"`
	PreviewBytes        int      `json:"previewBytes"`
	WatchChanges        bool     `json:"watchChanges"`
	ScanIntervalSeconds int      `json:"scanIntervalSeconds"`
}

// DefaultPolicy is used before the first check-in (the preview needs no sign-in).
var DefaultPolicy = Policy{
	Version:             "builtin",
	UploadLimitBytes:    100 << 20,
	ExcludeNames:        []string{"Thumbs.db", "desktop.ini", ".DS_Store", "~$*", "*.tmp", "~*.tmp"},
	ExcludeDirs:         []string{".git", "node_modules", "__pycache__", "$RECYCLE.BIN", "System Volume Information", ".Trash", "OneNote_RecycleBin"},
	PreviewExtensions:   []string{".csv", ".tsv", ".txt", ".dat", ".asc", ".xml", ".xsl", ".in", ".xlsx"},
	PreviewBytes:        2048,
	WatchChanges:        true,
	ScanIntervalSeconds: 900,
}

// Folder is a folder this machine syncs.
type Folder struct {
	ID         string   `json:"id"`
	Path       string   `json:"path"`
	Exclusions []string `json:"exclusions"`
	ScanDue    bool     `json:"scanDue"`
	LastScan   *struct {
		At            string `json:"at"`
		By            string `json:"by"`
		ByThisMachine bool   `json:"byThisMachine"`
	} `json:"lastScan"`
	Warning *struct {
		At     string `json:"at"`
		Reason string `json:"reason"`
	} `json:"warning"`
}

// Progress is the bar. Each file is in exactly one stage, and the stages add up to Total:
// ToUpload + Waiting + Processed + Attention. (Synced = Waiting + Processed + Attention, kept
// for servers that predate the stage counts.)
type Progress struct {
	Total           int   `json:"total"`
	Synced          int   `json:"synced"`
	Processed       int   `json:"processed"`
	Attention       int   `json:"attention"`
	NotInScope      int   `json:"notInScope"`
	NotProcessed    int   `json:"notProcessed"`
	UploadBytesLeft int64 `json:"uploadBytesLeft"`
	Counting        bool  `json:"counting"`
	// Found, not yet in Binaly.
	ToUpload int `json:"toUpload"`
	// In Binaly, waiting for the LLM; WaitingOnDrive of them are read from the drive.
	Waiting        int `json:"waiting"`
	WaitingOnDrive int `json:"waitingOnDrive"`
	// No longer in the folder; outside Total.
	Removed int `json:"removed"`
}

// HelloIn is the check-in request.
type HelloIn struct {
	InstallID   string            `json:"installId"`
	MachineName string            `json:"machineName"`
	OS          string            `json:"os"`
	Version     string            `json:"version"`
	DriveMap    map[string]string `json:"driveMap,omitempty"`
}

// HelloOut is the check-in answer.
type HelloOut struct {
	MachineID string `json:"machineId"`
	Workspace struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"workspace"`
	User *struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	} `json:"user"`
	Policy   Policy   `json:"policy"`
	Folders  []Folder `json:"folders"`
	Progress Progress `json:"progress"`
}

// Hello checks in.
func (c *Client) Hello(ctx context.Context, in HelloIn) (*HelloOut, error) {
	var out HelloOut
	return &out, c.do(ctx, "POST", "/sync/v1/hello", in, &out)
}

// AddFolder starts syncing a folder from this machine.
func (c *Client) AddFolder(ctx context.Context, machineID, path string) (*Folder, []string, error) {
	var out struct {
		Folder       Folder   `json:"folder"`
		AlsoSyncedBy []string `json:"alsoSyncedBy"`
	}
	err := c.do(ctx, "POST", "/sync/v1/folders", map[string]any{"machineId": machineID, "path": path}, &out)
	return &out.Folder, out.AlsoSyncedBy, err
}

// LeaveFolder stops syncing a folder from this machine.
func (c *Client) LeaveFolder(ctx context.Context, machineID, folderID string) error {
	return c.do(ctx, "DELETE", "/sync/v1/folders/"+url.PathEscape(folderID), map[string]any{"machineId": machineID}, nil)
}

// FileReport is one file as Sync saw it.
type FileReport struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Mtime   string `json:"mtime"`
	SHA256  string `json:"sha256"`
	Preview string `json:"preview,omitempty"`
	// Rendition "pdf": a OneNote section, sent as the PDF its OneNote app exported. Size and
	// SHA256 are the PDF's; Path and Mtime stay the section's.
	Rendition string `json:"rendition,omitempty"`
}

// ReportIn is one batch for one folder.
type ReportIn struct {
	MachineID           string       `json:"machineId"`
	FolderID            string       `json:"folderId"`
	ScanID              string       `json:"scanId"`
	Files               []FileReport `json:"files"`
	Deleted             []string     `json:"deleted,omitempty"`
	Full                bool         `json:"full,omitempty"`
	First               bool         `json:"first,omitempty"`
	Last                bool         `json:"last,omitempty"`
	ConfirmMassDeletion bool         `json:"confirmMassDeletion,omitempty"`
}

// FileResult is the server's answer for one file.
type FileResult struct {
	Path       string `json:"path"`
	Action     string `json:"action"`    // upload | known | register | excluded | rejected
	UploadURL  string `json:"uploadUrl"` // the server's relay
	StagingKey string `json:"stagingKey"`
	// DirectURL is a signed PUT straight to the lab's storage; the bytes skip the server.
	// DirectHeaders must be sent with it unchanged. Absent from older servers.
	DirectURL     string            `json:"directUrl"`
	DirectHeaders map[string]string `json:"directHeaders"`
	Processed     bool              `json:"processed"`
	Reason        string            `json:"reason"`
}

// ReportOut is the answer to a batch.
type ReportOut struct {
	PolicyVersion string       `json:"policyVersion"`
	Results       []FileResult `json:"results"`
	Gone          int          `json:"gone"`
	Swept         *int         `json:"swept"`
	Warning       string       `json:"warning"`
}

// Report sends one batch.
func (c *Client) Report(ctx context.Context, in ReportIn) (*ReportOut, error) {
	var out ReportOut
	return &out, c.do(ctx, "POST", "/sync/v1/report", in, &out)
}

// Commit finishes an upload.
func (c *Client) Commit(ctx context.Context, machineID, stagingKey, path, sha string) error {
	return c.do(ctx, "POST", "/sync/v1/commit", map[string]any{"machineId": machineID, "stagingKey": stagingKey, "path": path, "sha256": sha}, nil)
}

// StatusIn summarizes one run.
type StatusIn struct {
	MachineID  string         `json:"machineId"`
	OK         bool           `json:"ok"`
	DurationMs int64          `json:"durationMs"`
	Counts     map[string]int `json:"counts"`
	Errors     []string       `json:"errors,omitempty"`
	Unsynced   []string       `json:"unsynced,omitempty"`
}

// Status records a run summary.
func (c *Client) Status(ctx context.Context, in StatusIn) error {
	return c.do(ctx, "POST", "/sync/v1/status", in, nil)
}

// FolderProgress is one folder's bar.
type FolderProgress struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	Progress
	Warning *struct {
		Reason string `json:"reason"`
	} `json:"warning"`
}

// ProgressOut is the workspace's progress.
type ProgressOut struct {
	Overall Progress         `json:"overall"`
	Folders []FolderProgress `json:"folders"`
}

// GetProgress fetches the bar.
func (c *Client) GetProgress(ctx context.Context) (*ProgressOut, error) {
	var out ProgressOut
	return &out, c.do(ctx, "GET", "/sync/v1/progress", nil, &out)
}
