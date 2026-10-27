package main

import "testing"

func TestTrustedDocs(t *testing.T) {
	server := "https://mcp.binaly.ai"
	for u, want := range map[string]bool{
		"https://binaly.ai/docs/connect":       true,
		"https://app.binaly.ai/sync#assistant": true,
		"https://mcp.binaly.ai/docs":           true,
		"http://binaly.ai/docs":                false, // not https
		"https://binaly.ai.evil.example/":      false,
		"https://evil.example/binaly.ai":       false,
		"":                                     false,
	} {
		if got := trustedDocs(u, server); got != want {
			t.Errorf("trustedDocs(%q) = %v, want %v", u, got, want)
		}
	}
	if !trustedDocs("https://labs.example.org/guide", "https://labs.example.org") {
		t.Error("a self-hosted server's own address isn't trusted")
	}
}
