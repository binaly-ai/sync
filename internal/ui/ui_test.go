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
func (s *stub) FinishWizard(k string) error        { s.calls = append(s.calls, "done:"+k); return nil }
func (s *stub) Tree(k, p string) (any, error) {
	s.calls = append(s.calls, "tree:"+k+":"+p)
	return nil, nil
}
func (s *stub) Mark(k, p, m string) (any, error) {
	s.calls = append(s.calls, "mark:"+p+":"+m)
	return nil, nil
}
func (s *stub) Release(k, p string) (any, error) {
	s.calls = append(s.calls, "release:"+p)
	return nil, nil
}
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

func TestWizardRequestsAndScreen(t *testing.T) {
	s := &stub{}
	if _, err := Call(s, "wizard-done", []byte(`{"accountKey":"k1"}`)); err != nil {
		t.Fatal(err)
	}
	if strings.Join(s.calls, ",") != "done:k1" {
		t.Fatalf("calls %v", s.calls)
	}
	p := Page()
	for _, want := range []string{`id="view-welcome"`, "Connect your AI assistant", "Set up processing", "I'll do this later", "a computer that stays on", `"wizard-done"`} {
		if !strings.Contains(p, want) {
			t.Fatalf("page lacks %q", want)
		}
	}
}

func TestTreeRequestsAndScreen(t *testing.T) {
	s := &stub{}
	for _, c := range []struct{ name, body string }{
		{"tree", `{"accountKey":"k1","path":"X:/Lab"}`},
		{"mark", `{"accountKey":"k1","path":"X:/Lab/Data","mark":"first"}`},
		{"release", `{"accountKey":"k1","path":"X:/Lab/T"}`},
	} {
		if _, err := Call(s, c.name, []byte(c.body)); err != nil {
			t.Fatal(c.name, err)
		}
	}
	if _, err := Call(s, "mark", []byte(`{"accountKey":"k1","path":"X:/Lab","mark":"delete"}`)); err == nil {
		t.Fatal("an unknown mark was accepted")
	}
	if got := strings.Join(s.calls, ","); got != `tree:k1:X:/Lab,mark:X:/Lab/Data:first,release:X:/Lab/T` {
		t.Fatalf("calls %v", got)
	}
	p := Page()
	for _, want := range []string{`id="view-tree"`, `data-nav="tree"`, "Process first", "Leave out", "Read it", "not synced"} {
		if !strings.Contains(p, want) {
			t.Fatalf("page lacks %q", want)
		}
	}
}
