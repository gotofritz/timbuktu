package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAskCommand_missingArg(t *testing.T) {
	err := runCLI("ask")
	if err == nil {
		t.Fatal("expected error for missing question argument")
	}
}

func TestAskCommand_templateListCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := runCLI("init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	cfgPath := filepath.Join(home, ".tbuk", "config.yaml")

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runCLI("--config", cfgPath, "template", "list")

	_ = w.Close()
	os.Stdout = oldStdout

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	output := string(buf[:n])

	if err != nil {
		t.Fatalf("template list: %v", err)
	}
	// init wrote the qa template; list should show it
	if !strings.Contains(output, "qa") {
		t.Errorf("template list missing 'qa':\n%s", output)
	}
}

func TestAskCommand_templateShowCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := runCLI("init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	cfgPath := filepath.Join(home, ".tbuk", "config.yaml")

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runCLI("--config", cfgPath, "template", "show", "qa")

	_ = w.Close()
	os.Stdout = oldStdout

	buf := make([]byte, 8192)
	n, _ := r.Read(buf)
	output := string(buf[:n])

	if err != nil {
		t.Fatalf("template show qa: %v", err)
	}
	if !strings.Contains(output, "manifest") {
		t.Errorf("template show missing 'manifest':\n%s", output)
	}
	if !strings.Contains(output, "Question") {
		t.Errorf("template show missing 'Question' (user template):\n%s", output)
	}
}

func TestAskCommand_templateShowNotFound(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := runCLI("init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	cfgPath := filepath.Join(home, ".tbuk", "config.yaml")
	err := runCLI("--config", cfgPath, "template", "show", "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent template")
	}
}

func TestDoctorCommand_showsPrompts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := runCLI("init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	cfgPath := filepath.Join(home, ".tbuk", "config.yaml")

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runCLI("--config", cfgPath, "doctor")

	_ = w.Close()
	os.Stdout = oldStdout

	buf := make([]byte, 8192)
	n, _ := r.Read(buf)
	output := string(buf[:n])

	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if !strings.Contains(output, "Prompts") {
		t.Errorf("doctor output missing Prompts section:\n%s", output)
	}
	if !strings.Contains(output, "qa") {
		t.Errorf("doctor output missing 'qa' template:\n%s", output)
	}
}
