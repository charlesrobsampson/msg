package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/charlesrobsampson/msg/client"
)

func serverPIDsPath() (string, error) {
	dir, err := client.ConfigDirPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "server-pids.json"), nil
}

func loadServerPIDs() map[string]int {
	path, err := serverPIDsPath()
	if err != nil {
		return map[string]int{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]int{}
	}
	var pids map[string]int
	if json.Unmarshal(data, &pids) != nil {
		return map[string]int{}
	}
	return pids
}

func saveServerPIDs(pids map[string]int) {
	path, err := serverPIDsPath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return
	}
	data, _ := json.MarshalIndent(pids, "", "  ")
	os.WriteFile(path, data, 0644)
}

// isServerPIDAlive returns true if we have a recorded PID that is still running.
func isServerPIDAlive(configKey string) bool {
	pids := loadServerPIDs()
	pid, ok := pids[configKey]
	if !ok || pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// startProviderServer launches serverCmd in the background and records its PID.
func startProviderServer(configKey, serverCmd string) error {
	parts := strings.Fields(serverCmd)
	if len(parts) == 0 {
		return fmt.Errorf("server_cmd is empty")
	}
	cmd := exec.Command(parts[0], parts[1:]...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	if err := cmd.Start(); err != nil {
		return err
	}
	pids := loadServerPIDs()
	pids[configKey] = cmd.Process.Pid
	saveServerPIDs(pids)
	return nil
}

// stopProviderServer sends SIGTERM to the recorded PID, or falls back to lsof on the given port.
func stopProviderServer(configKey string, port int) error {
	pids := loadServerPIDs()
	pid, hasPID := pids[configKey]

	if hasPID && pid > 0 {
		if proc, err := os.FindProcess(pid); err == nil {
			proc.Signal(syscall.SIGTERM)
		}
		delete(pids, configKey)
		saveServerPIDs(pids)
		return nil
	}

	// No stored PID — find the process by port using lsof.
	if port <= 0 {
		return fmt.Errorf("no PID on record and no port configured to search")
	}
	out, err := exec.Command("lsof", "-i", fmt.Sprintf(":%d", port), "-t").Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return fmt.Errorf("could not find a process on port %d", port)
	}
	var killed int
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if p, err := strconv.Atoi(strings.TrimSpace(line)); err == nil {
			if proc, err := os.FindProcess(p); err == nil {
				proc.Signal(syscall.SIGTERM)
				killed++
			}
		}
	}
	if killed == 0 {
		return fmt.Errorf("no processes found on port %d", port)
	}
	return nil
}
