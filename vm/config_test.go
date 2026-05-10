package vm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigValidateInitializesEnvironmentVariables(t *testing.T) {
	paths := writeFirecrackerFixtures(t)
	cfg := Config{
		Firecracker: FirecrackerConfig{
			FirecrackerBin: paths.firecrackerBin,
			KernelImage:    paths.kernelImage,
			InitrdImage:    paths.initrdImage,
			WorkDir:        paths.workDir,
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.EnvironmentVariables == nil {
		t.Fatal("expected EnvironmentVariables to be initialized")
	}
	if got := cfg.EnvironmentVariables.Len(); got != 0 {
		t.Fatalf("EnvironmentVariables.Len() = %d", got)
	}
}

func TestConfigValidatePreservesProvidedEnvironmentVariables(t *testing.T) {
	paths := writeFirecrackerFixtures(t)
	envs := NewEnvironmentVariables()
	defer envs.Destroy()
	if err := envs.Set("FOO", "bar"); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		Firecracker: FirecrackerConfig{
			FirecrackerBin: paths.firecrackerBin,
			KernelImage:    paths.kernelImage,
			InitrdImage:    paths.initrdImage,
			WorkDir:        paths.workDir,
		},
		EnvironmentVariables: envs,
	}

	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.EnvironmentVariables != envs {
		t.Fatal("expected provided EnvironmentVariables pointer to be preserved")
	}
	if got, ok := cfg.EnvironmentVariables.get("FOO"); !ok || got != "bar" {
		t.Fatalf("get(FOO) = %q, %v", got, ok)
	}
}

func TestConfigInitInitializesEnvironmentVariables(t *testing.T) {
	paths := writeFirecrackerFixtures(t)
	cfg := Config{
		Firecracker: FirecrackerConfig{
			FirecrackerBin: paths.firecrackerBin,
			KernelImage:    paths.kernelImage,
			InitrdImage:    paths.initrdImage,
			WorkDir:        paths.workDir,
		},
	}

	if err := cfg.Init(); err != nil {
		t.Fatal(err)
	}
	if cfg.EnvironmentVariables == nil {
		t.Fatal("expected EnvironmentVariables to be initialized")
	}
	if cfg.Firecracker.WorkDir != paths.workDir {
		t.Fatalf("WorkDir = %q, want %q", cfg.Firecracker.WorkDir, paths.workDir)
	}
}

func TestFirecrackerConfigInitUsesEnvironmentDefaults(t *testing.T) {
	paths := writeFirecrackerFixtures(t)
	t.Setenv("FIRECRACKER_BIN", paths.firecrackerBin)
	t.Setenv("KERNEL_IMAGE", paths.kernelImage)
	t.Setenv("INITRD_IMAGE", paths.initrdImage)
	t.Setenv("WORK_DIR", paths.workDir)

	var cfg FirecrackerConfig
	if err := cfg.Init(); err != nil {
		t.Fatal(err)
	}

	if cfg.FirecrackerBin != paths.firecrackerBin {
		t.Fatalf("FirecrackerBin = %q, want %q", cfg.FirecrackerBin, paths.firecrackerBin)
	}
	if cfg.KernelImage != paths.kernelImage {
		t.Fatalf("KernelImage = %q, want %q", cfg.KernelImage, paths.kernelImage)
	}
	if cfg.InitrdImage != paths.initrdImage {
		t.Fatalf("InitrdImage = %q, want %q", cfg.InitrdImage, paths.initrdImage)
	}
	if cfg.WorkDir != paths.workDir {
		t.Fatalf("WorkDir = %q, want %q", cfg.WorkDir, paths.workDir)
	}
}

type firecrackerFixturePaths struct {
	firecrackerBin string
	kernelImage    string
	initrdImage    string
	workDir        string
}

func writeFirecrackerFixtures(t *testing.T) firecrackerFixturePaths {
	t.Helper()

	baseDir := t.TempDir()
	artifactsDir := filepath.Join(baseDir, "artifacts")
	workDir := filepath.Join(baseDir, "run")
	if err := os.MkdirAll(artifactsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	firecrackerBin := filepath.Join(baseDir, "firecracker")
	kernelImage := filepath.Join(artifactsDir, "vmlinux")
	initrdImage := filepath.Join(artifactsDir, "initramfs.cpio.gz")
	for _, path := range []string{firecrackerBin, kernelImage, initrdImage} {
		if err := os.WriteFile(path, []byte("fixture"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	return firecrackerFixturePaths{
		firecrackerBin: firecrackerBin,
		kernelImage:    kernelImage,
		initrdImage:    initrdImage,
		workDir:        workDir,
	}
}
