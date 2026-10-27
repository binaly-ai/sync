package ui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// stub records the requests the page makes; only the ones under test do anything.
type stub struct{ calls []string }

func (s *stub) Status() any                        { return nil }
func (s *stub) Preview(string) (any, error)        { return nil, nil }
func (s *stub) Start(string, string) error         { return nil }
func (s *stub) SyncNow()                           {}
func (s *stub) Pause(time.Duration)                {}
func (s *stub) Resume()                            {}
func (s *stub) Settings() any                      { return nil }
func (s *stub) SaveSettings(json.RawMessage) error { return nil }
func (s *stub) SignIn() error                      { return nil }
func (s *stub) SignOut(string) error               { return nil }
func (s *stub) PickFolder() (string, error)        { return "", nil }
func (s *stub) ConfirmDeletions(string)            {}
func (s *stub) OpenLog()                           {}
func (s *stub) InstallUpdate()                     {}
func (s *stub) CheckUpdate()                       {}
func (s *stub) OpenSetup(string)                   {}
func (s *stub) OpenInBinaly(string)                {}
func (s *stub) RemoveFolder(string, string) error  { return nil }
func (s *stub) OpenURL(string)                     {}
func (s *stub) OpenDocs(k string)                  { s.calls = append(s.calls, "docs:"+k) }
func (s *stub) Assistant(k string) (any, error) {
	s.calls = append(s.calls, "assistant:"+k)
	return map[string]string{"connectorUrl": "https://mcp.binaly.ai/mcp"}, nil
}

func TestAssistantRequests(t *testing.T) {
	s := &stub{}
	out, err := Call(s, "assistant", []byte(`{"accountKey":"k1"}`))
	if err != nil || out.(map[string]string)["connectorUrl"] == "" {
		t.Fatalf("assistant: %v %v", out, err)
	}
	if _, err := Call(s, "open-docs", []byte(`{"accountKey":"k1"}`)); err != nil {
		t.Fatal(err)
	}
	if strings.Join(s.calls, ",") != "assistant:k1,docs:k1" {
		t.Fatalf("calls %v", s.calls)
	}
}

func TestPageHasTheAssistantScreenAndTheSensitiveNote(t *testing.T) {
	p := Page()
	for _, want := range []string{`id="view-assistant"`, `data-nav="assistant"`, "peer reviews you're writing for a journal", "it can't recognize everything"} {
		if !strings.Contains(p, want) {
			t.Fatalf("page lacks %q", want)
		}
	}
	if strings.Contains(p, "—") {
		t.Fatal("the page has an em dash")
	}
}
