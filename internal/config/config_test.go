package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWizardFlagsPersist(t *testing.T) {
	t.Setenv("BINALY_SYNC_HOME", t.TempDir())
	s, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	if !s.WizardPending() {
		t.Fatal("a new install doesn't open the wizard")
	}
	s.MarkSetupPending("k1")
	s.MarkSetupPending("k1")
	s.MarkSetupPending("k2")
	if got := s.SetupPending(); len(got) != 2 {
		t.Fatalf("pending %v", got)
	}
	if err := s.FinishWizard("k1"); err != nil {
		t.Fatal(err)
	}
	again, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	if again.WizardPending() {
		t.Fatal("the wizard opens again after it was finished")
	}
	if got := again.SetupPending(); len(got) != 1 || got[0] != "k2" {
		t.Fatalf("pending after reopening %v, want [k2]", got)
	}
	again.RemoveAccount("k2")
	if len(again.SetupPending()) != 0 {
		t.Fatal("a signed-out workspace stays pending")
	}
}

func TestInstallFromBeforeTheWizardNeverSeesIt(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BINALY_SYNC_HOME", dir)
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"installId":"inst-1","accounts":[{"server":"s","workspaceId":"w"}],"settings":{}}`), 0o600)
	s, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	if s.WizardPending() {
		t.Fatal("an existing install was shown the first-launch wizard")
	}
}
