package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func findProjectRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	// wd may be cmd/undined or project root
	dir := wd
	for i := 0; i < 5; i++ {
		if _, err := os.Stat(filepath.Join(dir, "scripts", "install_gemini_skill.sh")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("could not find project root containing scripts/install_gemini_skill.sh")
}

func TestInstallerScriptPermissionsAndSymlink(t *testing.T) {
	root, err := findProjectRoot()
	if err != nil {
		t.Fatalf("%v", err)
	}

	scriptPath := filepath.Join(root, "scripts", "install_gemini_skill.sh")
	symlinkPath := filepath.Join(root, "scripts", "install_agy_skill.sh")

	info, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatalf("script %s does not exist: %v", scriptPath, err)
	}
	if info.Mode()&0111 == 0 {
		t.Errorf("expected script %s to be executable, mode: %v", scriptPath, info.Mode())
	}

	symlinkInfo, err := os.Lstat(symlinkPath)
	if err != nil {
		t.Fatalf("symlink %s does not exist: %v", symlinkPath, err)
	}
	if symlinkInfo.Mode()&os.ModeSymlink == 0 {
		t.Errorf("expected %s to be a symlink", symlinkPath)
	}

	dest, err := os.Readlink(symlinkPath)
	if err != nil {
		t.Fatalf("failed reading symlink: %v", err)
	}
	if dest != "install_gemini_skill.sh" && filepath.Base(dest) != "install_gemini_skill.sh" {
		t.Errorf("expected symlink target to be install_gemini_skill.sh, got: %s", dest)
	}
}

func TestInstallerHelp(t *testing.T) {
	root, err := findProjectRoot()
	if err != nil {
		t.Fatalf("%v", err)
	}

	scriptPath := filepath.Join(root, "scripts", "install_gemini_skill.sh")
	cmd := exec.Command(scriptPath, "--help")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--help failed: %v, output: %s", err, string(out))
	}

	output := string(out)
	for _, expected := range []string{
		"Usage:",
		"--link",
		"--copy",
		"--skill",
		"--skip-mcp",
		"agy --print",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("expected help output to contain %q, output:\n%s", expected, output)
		}
	}
}

func TestInstallerMockAgy(t *testing.T) {
	root, err := findProjectRoot()
	if err != nil {
		t.Fatalf("%v", err)
	}

	tmpDir := t.TempDir()
	mockBin := filepath.Join(tmpDir, "bin")
	_ = os.MkdirAll(mockBin, 0755)

	logFile := filepath.Join(tmpDir, "agy_calls.log")
	mockAgy := filepath.Join(mockBin, "agy")
	mockAgyScript := fmt.Sprintf("#!/bin/sh\necho \"$@\" >> %q\nexit 0\n", logFile)
	if err := os.WriteFile(mockAgy, []byte(mockAgyScript), 0755); err != nil {
		t.Fatalf("failed creating mock agy: %v", err)
	}

	targetSkillsDir := filepath.Join(tmpDir, "gemini_skills")
	scriptPath := filepath.Join(root, "scripts", "install_gemini_skill.sh")

	cmd := exec.Command(scriptPath, "--no-trust")
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("PATH=%s:%s", mockBin, os.Getenv("PATH")),
		fmt.Sprintf("AGY_SKILLS_DIR=%s", targetSkillsDir),
	)

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("installer execution failed: %v, output: %s", err, string(out))
	}

	// Verify calls to agy
	callsData, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("failed reading agy calls log: %v", err)
	}
	callsStr := string(callsData)
	if !strings.Contains(callsStr, "mcp add undine") || !strings.Contains(callsStr, "--mcp-stdio") {
		t.Errorf("expected agy call to contain 'mcp add undine' and '--mcp-stdio', got:\n%s", callsStr)
	}

	// Verify symlinks created in target skills dir
	undineReview := filepath.Join(targetSkillsDir, "undine-review")
	reviewInfo, err := os.Lstat(undineReview)
	if err != nil || reviewInfo.Mode()&os.ModeSymlink == 0 {
		t.Errorf("expected undine-review symlink in %s", targetSkillsDir)
	}
	if _, err := os.Stat(filepath.Join(undineReview, "SKILL.md")); err != nil {
		t.Errorf("expected SKILL.md in undine-review: %v", err)
	}

	undineMCP := filepath.Join(targetSkillsDir, "undine-mcp-review")
	mcpInfo, err := os.Lstat(undineMCP)
	if err != nil || mcpInfo.Mode()&os.ModeSymlink == 0 {
		t.Errorf("expected undine-mcp-review symlink in %s", targetSkillsDir)
	}
	if _, err := os.Stat(filepath.Join(undineMCP, "SKILL.md")); err != nil {
		t.Errorf("expected SKILL.md in undine-mcp-review: %v", err)
	}
}

func TestInstallerCopyMode(t *testing.T) {
	root, err := findProjectRoot()
	if err != nil {
		t.Fatalf("%v", err)
	}

	tmpDir := t.TempDir()
	mockBin := filepath.Join(tmpDir, "bin")
	_ = os.MkdirAll(mockBin, 0755)

	mockAgy := filepath.Join(mockBin, "agy")
	mockAgyScript := "#!/bin/sh\nexit 0\n"
	_ = os.WriteFile(mockAgy, []byte(mockAgyScript), 0755)

	targetSkillsDir := filepath.Join(tmpDir, "copy_skills")
	scriptPath := filepath.Join(root, "scripts", "install_gemini_skill.sh")

	cmd := exec.Command(scriptPath, "--copy", "--skip-mcp", "--no-trust", "--quiet")
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("PATH=%s:%s", mockBin, os.Getenv("PATH")),
		fmt.Sprintf("AGY_SKILLS_DIR=%s", targetSkillsDir),
	)

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("installer execution failed: %v, output: %s", err, string(out))
	}

	// Verify real directory (not symlink) was created
	for _, skill := range []string{"undine-review", "undine-mcp-review"} {
		skillDir := filepath.Join(targetSkillsDir, skill)
		info, err := os.Lstat(skillDir)
		if err != nil {
			t.Fatalf("expected skill dir %s: %v", skillDir, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			t.Errorf("expected %s to be regular directory, not symlink", skillDir)
		}
		if !info.IsDir() {
			t.Errorf("expected %s to be directory", skillDir)
		}
		if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err != nil {
			t.Errorf("expected SKILL.md in %s", skillDir)
		}
	}
}
