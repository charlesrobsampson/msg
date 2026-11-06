package client

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTestConfig(t *testing.T, home, filename, body string) {
	t.Helper()
	dir := filepath.Join(home, configDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", filename, err)
	}
}

func TestForwardingConfig(t *testing.T) {
	t.Run("absent is disabled", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		writeTestConfig(t, home, settingsFileName, `{"account":"+15555550100"}`)

		cfg, err := LoadConfigForProfile("")
		if err != nil {
			t.Fatalf("LoadConfigForProfile: %v", err)
		}
		if cfg.Forwarding.Enabled {
			t.Fatal("forwarding enabled without a forwarding object")
		}
		if cfg.Forwarding.URL != "" {
			t.Fatalf("URL = %q, want empty", cfg.Forwarding.URL)
		}
	})

	t.Run("enabled with url loads and is preserved exactly", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		const url = "http://127.0.0.1:8080/receive?x=1&y=2"
		writeTestConfig(t, home, settingsFileName, `{
			"account": "+15555550100",
			"forwarding": {"enabled": true, "url": "`+url+`"}
		}`)

		cfg, err := LoadConfigForProfile("")
		if err != nil {
			t.Fatalf("LoadConfigForProfile: %v", err)
		}
		if !cfg.Forwarding.Enabled {
			t.Fatal("forwarding.Enabled = false, want true")
		}
		if cfg.Forwarding.URL != url {
			t.Fatalf("forwarding.URL = %q, want %q", cfg.Forwarding.URL, url)
		}
	})

	t.Run("profiles are independent", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		writeTestConfig(t, home, settingsFileName, `{
			"account": "+15555550100",
			"forwarding": {"enabled": true, "url": "http://default.example/receive"}
		}`)
		writeTestConfig(t, home, "config-alice.json", `{
			"account": "+15555550101",
			"forwarding": {"enabled": false, "url": "http://alice.example/receive"}
		}`)

		defaultCfg, err := LoadConfigForProfile("")
		if err != nil {
			t.Fatalf("LoadConfigForProfile(default): %v", err)
		}
		if !defaultCfg.Forwarding.Enabled || defaultCfg.Forwarding.URL != "http://default.example/receive" {
			t.Fatalf("default forwarding = %+v", defaultCfg.Forwarding)
		}

		aliceCfg, err := LoadConfigForProfile("alice")
		if err != nil {
			t.Fatalf("LoadConfigForProfile(alice): %v", err)
		}
		if aliceCfg.Forwarding.Enabled || aliceCfg.Forwarding.URL != "http://alice.example/receive" {
			t.Fatalf("alice forwarding = %+v", aliceCfg.Forwarding)
		}

		target, err := ForwardingForAccount("+15555550100")
		if err != nil {
			t.Fatalf("ForwardingForAccount(default): %v", err)
		}
		if !target.Enabled || target.URL != "http://default.example/receive" {
			t.Fatalf("default account forwarding = %+v", target)
		}

		target, err = ForwardingForAccount("+15555550101")
		if err != nil {
			t.Fatalf("ForwardingForAccount(alice): %v", err)
		}
		if target.Enabled {
			t.Fatalf("alice account forwarding = %+v, want disabled", target)
		}
	})

	t.Run("malformed json returns an error", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		writeTestConfig(t, home, "config-bob.json", `{"forwarding":`)

		cfg, err := LoadConfigForProfile("bob")
		if err == nil {
			t.Fatal("LoadConfigForProfile() error = nil for malformed JSON")
		}
		if cfg.Forwarding.Enabled {
			t.Fatal("malformed config enabled forwarding")
		}
	})

	t.Run("missing file returns disabled defaults", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)

		cfg, err := LoadConfigForProfile("nobody")
		if err != nil {
			t.Fatalf("LoadConfigForProfile() error = %v, want nil", err)
		}
		if cfg.Forwarding.Enabled {
			t.Fatal("missing config enabled forwarding")
		}
	})
}
