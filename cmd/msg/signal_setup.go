package main

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/charlesrobsampson/msg/client"
)

//go:embed signal-setup/Dockerfile.signal-patched
var signalDockerfile []byte

//go:embed signal-setup/docker-compose-signal.yml
var signalComposeYML []byte

// writeSignalSetupFiles writes the Dockerfile and compose file to ~/.config/msg/
// if they do not already exist. Safe to call on every enable — existing files
// are never overwritten so local edits are preserved.
func writeSignalSetupFiles() error {
	cfgDir, err := client.ConfigDirPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	files := []struct {
		name    string
		content []byte
	}{
		{"Dockerfile.signal-patched", signalDockerfile},
		{"docker-compose-signal.yml", signalComposeYML},
	}

	for _, f := range files {
		dest := filepath.Join(cfgDir, f.name)
		if _, err := os.Stat(dest); os.IsNotExist(err) {
			if err := os.WriteFile(dest, f.content, 0644); err != nil {
				return fmt.Errorf("writing %s: %w", f.name, err)
			}
		}
	}
	return nil
}
