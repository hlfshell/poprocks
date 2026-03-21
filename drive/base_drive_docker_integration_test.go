package drive

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// This integration test executes real-action scenarios inside dedicated
// containers configured for each path we want to validate.
func TestBaseDriveDockerIntegration_ScenarioRunners(t *testing.T) {
	tmpDir := t.TempDir()
	if err := ensureFixtureImageBuilt(); err != nil {
		t.Fatalf("failed to build fixture image: %v", err)
	}
	if err := copyFixtureImagesFromContainer(tmpDir); err != nil {
		t.Fatalf("failed to export fixture images: %v", err)
	}

	if err := buildRunnerImage("microvm-drive-runner-guestmount:latest", filepath.Join("..", "test_assets", "drive_test_runner_guestmount")); err != nil {
		t.Fatalf("failed to build guestmount runner image: %v", err)
	}
	if err := buildRunnerImage("microvm-drive-runner-rootloop:latest", filepath.Join("..", "test_assets", "drive_test_runner_rootloop")); err != nil {
		t.Fatalf("failed to build root-loop runner image: %v", err)
	}

	scenarios := []struct {
		name      string
		image     string
		scenario  string
		extraArgs []string
	}{
		{
			name:     "guestmount_nonroot_ext4_rw",
			image:    "microvm-drive-runner-guestmount:latest",
			scenario: "guestmount_nonroot_ext4_rw",
			extraArgs: []string{
				"--device", "/dev/fuse",
				"--cap-add", "SYS_ADMIN",
				"--security-opt", "apparmor=unconfined",
				"--security-opt", "seccomp=unconfined",
			},
		},
		{
			name:     "guestmount_auto_ext4_rw",
			image:    "microvm-drive-runner-guestmount:latest",
			scenario: "guestmount_auto_ext4_rw",
			extraArgs: []string{
				"--device", "/dev/fuse",
				"--cap-add", "SYS_ADMIN",
				"--security-opt", "apparmor=unconfined",
				"--security-opt", "seccomp=unconfined",
			},
		},
		{
			name:     "auto_detect_none_available",
			image:    "microvm-drive-runner-guestmount:latest",
			scenario: "auto_detect_none_available",
			extraArgs: []string{
				"--device", "/dev/fuse",
				"--cap-add", "SYS_ADMIN",
				"--security-opt", "apparmor=unconfined",
				"--security-opt", "seccomp=unconfined",
			},
		},
		{
			name:      "root_loop_ext4_rw",
			image:     "microvm-drive-runner-rootloop:latest",
			scenario:  "root_loop_ext4_rw",
			extraArgs: []string{"--privileged"},
		},
		{
			name:      "squashfs_readonly",
			image:     "microvm-drive-runner-rootloop:latest",
			scenario:  "squashfs_readonly",
			extraArgs: []string{"--privileged"},
		},
		{
			name:      "delete_ephemeral",
			image:     "microvm-drive-runner-rootloop:latest",
			scenario:  "delete_ephemeral",
			extraArgs: []string{"--privileged"},
		},
		{
			name:      "luks_dmcrypt_ext4_rw",
			image:     "microvm-drive-runner-rootloop:latest",
			scenario:  "luks_dmcrypt_ext4_rw",
			extraArgs: []string{"--privileged"},
		},
	}

	for _, tc := range scenarios {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"run", "--rm"}
			args = append(args, tc.extraArgs...)
			args = append(
				args,
				"-e", "SCENARIO="+tc.scenario,
				"-e", "FIXTURE_DIR=/fixtures",
				"-v", fmt.Sprintf("%s:/fixtures:ro", tmpDir),
				tc.image,
			)
			cmd := exec.Command("docker", args...)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("scenario %s failed: %v\n%s", tc.scenario, err, string(output))
			}
		})
	}
}

func ensureFixtureImageBuilt() error {
	if imageExists("microvm-drive-fixtures:latest") {
		return nil
	}
	contextDir := filepath.Join("..", "test_assets", "drive_fixture_server")
	if _, err := os.Stat(filepath.Join(contextDir, "Dockerfile")); err != nil {
		return fmt.Errorf("fixture dockerfile missing: %w", err)
	}
	cmd := exec.Command("docker", "build", "-t", "microvm-drive-fixtures:latest", contextDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker build failed: %w: %s", err, string(output))
	}
	return nil
}

func copyFixtureImagesFromContainer(destinationDir string) error {
	containerName := "microvm-drive-fixtures-export"
	createCmd := exec.Command("docker", "create", "--name", containerName, "microvm-drive-fixtures:latest")
	createOut, err := createCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker create failed: %w: %s", err, string(createOut))
	}
	defer exec.Command("docker", "rm", "-f", containerName).Run()

	ext4Dest := filepath.Join(destinationDir, "ext4.img")
	squashDest := filepath.Join(destinationDir, "squashfs.img")
	if output, err := exec.Command("docker", "cp", containerName+":/assets/ext4.img", ext4Dest).CombinedOutput(); err != nil {
		return fmt.Errorf("docker cp ext4 failed: %w: %s", err, string(output))
	}
	if output, err := exec.Command("docker", "cp", containerName+":/assets/squashfs.img", squashDest).CombinedOutput(); err != nil {
		return fmt.Errorf("docker cp squashfs failed: %w: %s", err, string(output))
	}
	return nil
}

func buildRunnerImage(tag string, contextDir string) error {
	dockerfilePath := filepath.Join(contextDir, "Dockerfile")
	if _, err := os.Stat(dockerfilePath); err != nil {
		return fmt.Errorf("runner dockerfile missing: %w", err)
	}
	// Build with repo root as context so Dockerfile COPY can access drive source.
	cmd := exec.Command("docker", "build", "-f", dockerfilePath, "-t", tag, "..")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker build failed for %s: %w: %s", tag, err, string(output))
	}
	return nil
}

func imageExists(tag string) bool {
	cmd := exec.Command("docker", "image", "inspect", tag)
	return cmd.Run() == nil
}
