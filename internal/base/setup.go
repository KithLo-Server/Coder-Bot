package base

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// ConfigureGit prepares the base container's global git identity and SSH signing
// the same way the worker and shell entrypoints do.
func ConfigureGit(sshKey, userName, userEmail string) error {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "/root"
	}
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		return err
	}
	if sshKey != "" {
		keyPath := filepath.Join(home, ".ssh", "opencode_key")
		if err := os.WriteFile(keyPath, []byte(sshKey), 0o600); err != nil {
			return err
		}
		settings := [][2]string{
			{"user.signingkey", keyPath},
			{"commit.gpgsign", "true"},
			{"gpg.format", "ssh"},
			{"core.sshCommand", "ssh -i " + keyPath + " -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new"},
		}
		for _, setting := range settings {
			if err := git("config", "--global", setting[0], setting[1]); err != nil {
				return err
			}
		}
	}
	if userName != "" {
		if err := git("config", "--global", "user.name", userName); err != nil {
			return err
		}
	}
	if userEmail != "" {
		if err := git("config", "--global", "user.email", userEmail); err != nil {
			return err
		}
	}
	return nil
}

func git(args ...string) error {
	command := exec.Command("git", args...)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("git %v: %w: %s", args, err, string(output))
	}
	return nil
}
