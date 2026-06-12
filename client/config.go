package client

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Alias struct {
	Shortcut       string `json:"shortcut"`
	Name           string `json:"name"`
	Number         string `json:"number"`
	ConversationID string `json:"conversation_id"`
	Platform       string `json:"platform"`
}

type ThemeConfig struct {
	PrimaryColor     string `json:"primary_color"`
	SubtleColor      string `json:"subtle_color"`
	MeMessageColor   string `json:"me_message_color"`
	MainTextColor    string `json:"main_text_color"`  // Added
	BackgroundColor  string `json:"background_color"` // Added
	TimestampColor   string `json:"timestamp_color"`
	NameColorPalette string `json:"name_color_palette"` // Added: "vibrant", "pastel", "muted"
}

type ProviderSettings struct {
	Type      string `json:"type"`
	Enabled   bool   `json:"enabled"`
	URL       string `json:"url,omitempty"`
	Port      int    `json:"port,omitempty"`
	ServerCmd string `json:"server_cmd,omitempty"` // shell command used to start this provider's server
}

// ConfigDirPath returns the absolute path to the msg config directory.
func ConfigDirPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, configDirName), nil
}

type Config struct {
	Account        string                      `json:"account,omitempty"`
	Editor         string                      `json:"editor"`
	ScrollSpeed    int                         `json:"scroll_speed"`
	ViewportHeight int                         `json:"viewport_height"`
	Theme          ThemeConfig                 `json:"theme"`
	Providers      map[string]ProviderSettings `json:"providers"`
}

const configDirName = ".config/msg"
const aliasFileName = "aliases.json"
const settingsFileName = "config.json"

var currentProfile string

func SetProfile(name string) {
	currentProfile = name
}

func GetProfile() string {
	return currentProfile
}

func GetConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	filename := aliasFileName
	if currentProfile != "" {
		filename = "aliases-" + currentProfile + ".json"
	}
	return filepath.Join(home, configDirName, filename), nil
}

func GetSettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	filename := settingsFileName
	if currentProfile != "" {
		filename = "config-" + currentProfile + ".json"
	}
	return filepath.Join(home, configDirName, filename), nil
}

func GetDraftPath(convID string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	draftDir := "drafts"
	if currentProfile != "" {
		draftDir = "drafts-" + currentProfile
	}
	return filepath.Join(home, configDirName, draftDir, convID+".md"), nil
}

func SaveDraft(convID, body string) error {
	path, err := GetDraftPath(convID)
	if err != nil {
		return err
	}
	err = os.MkdirAll(filepath.Dir(path), 0755)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0644)
}

func LoadDraft(convID string) (string, error) {
	path, err := GetDraftPath(convID)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func DeleteDraft(convID string) error {
	path, err := GetDraftPath(convID)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	return os.Remove(path)
}

func NewDefaultConfig() Config {
	return Config{
		Editor:         "nvim",
		ScrollSpeed:    1,
		ViewportHeight: 10,
		Theme: ThemeConfig{
			PrimaryColor:     "6", // Terminal Cyan
			SubtleColor:      "8", // Terminal Bright Black (Gray)
			MeMessageColor:   "4", // Terminal Blue
			MainTextColor:    "",  // Inherit
			BackgroundColor:  "",  // Inherit
			TimestampColor:   "8", // Terminal Bright Black (Gray)
			NameColorPalette: "pastel",
		},
		Providers: map[string]ProviderSettings{
			"sms": {
				Type:    "sms",
				Enabled: true,
				Port:    7007,
			},
			"signal": {
				Type:    "signal",
				Enabled: false,
				Port:    18081,
			},
		},
	}
}

// FindProfileForAccount returns the profile name whose config has Account == account.
// Returns "" for the default profile, or an error if no profile matches.
func FindProfileForAccount(account string) (string, error) {
	home, _ := os.UserHomeDir()
	configDir := filepath.Join(home, configDirName)

	// Check default profile first.
	if data, err := os.ReadFile(filepath.Join(configDir, settingsFileName)); err == nil {
		var cfg Config
		if json.Unmarshal(data, &cfg) == nil && cfg.Account == account {
			return "", nil
		}
	}

	// Scan config-<profile>.json files.
	entries, _ := os.ReadDir(configDir)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "config-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		profileName := name[len("config-") : len(name)-len(".json")]
		data, err := os.ReadFile(filepath.Join(configDir, name))
		if err != nil {
			continue
		}
		var cfg Config
		if json.Unmarshal(data, &cfg) == nil && cfg.Account == account {
			return profileName, nil
		}
	}

	return "", fmt.Errorf("no profile found for account %s", account)
}

func LoadConfig() (Config, error) {
	defaultConfig := NewDefaultConfig()
	path, err := GetSettingsPath()
	if err != nil {
		return defaultConfig, err
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return defaultConfig, nil
	}

	fileData, err := os.ReadFile(path)
	if err != nil {
		return defaultConfig, err
	}

	var cfg Config
	if err := json.Unmarshal(fileData, &cfg); err != nil {
		return defaultConfig, fmt.Errorf("malformed config file at %s: %v", path, err)
	}

	dirty := false

	// Migrate legacy "openmessage" provider key to "sms".
	if om, ok := cfg.Providers["openmessage"]; ok {
		if cfg.Providers == nil {
			cfg.Providers = map[string]ProviderSettings{}
		}
		sms := cfg.Providers["sms"]
		sms.Type = "sms"
		if !sms.Enabled {
			sms.Enabled = om.Enabled
		}
		if sms.Port == 0 {
			sms.Port = om.Port
		}
		if sms.URL == "" {
			sms.URL = om.URL
		}
		cfg.Providers["sms"] = sms
		delete(cfg.Providers, "openmessage")
		dirty = true
	}

	// Ensure default ports are written explicitly so users can see and edit them.
	defaultPorts := map[string]int{"sms": 7007, "signal": 18081}
	for key, defaultPort := range defaultPorts {
		if ps, ok := cfg.Providers[key]; ok && ps.Port == 0 {
			ps.Port = defaultPort
			cfg.Providers[key] = ps
			dirty = true
		}
	}

	if dirty {
		_ = SaveConfig(cfg) // best-effort rewrite
	}

	return cfg, nil
}

func SaveConfig(cfg Config) error {
	path, err := GetSettingsPath()
	if err != nil {
		return err
	}

	err = os.MkdirAll(filepath.Dir(path), 0755)
	if err != nil {
		return err
	}

	jsonData, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, jsonData, 0644)
}

func LoadAliases() (map[string]Alias, error) {
	aliases := make(map[string]Alias)
	path, err := GetConfigPath()
	if err != nil {
		return aliases, err
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return aliases, nil
	}

	fileData, err := os.ReadFile(path)
	if err != nil {
		return aliases, err
	}

	if err = json.Unmarshal(fileData, &aliases); err != nil {
		return aliases, err
	}

	// Normalise legacy alias entries so that:
	//   - conversation_id is always a prefixed ID (e.g. "sms:192", "signal:+…")
	//   - the map key equals that prefixed conversation_id
	normalised := make(map[string]Alias, len(aliases))
	dirty := false
	for key, a := range aliases {
		// Ensure conversation_id carries the platform prefix.
		if a.Platform != "" && !strings.HasPrefix(a.ConversationID, a.Platform+":") {
			a.ConversationID = a.Platform + ":" + a.ConversationID
			dirty = true
		}
		// Canonical key = conversation_id (strip any double-prefix from old keys).
		correctKey := a.ConversationID
		if key != correctKey {
			dirty = true
		}
		normalised[correctKey] = a
	}
	if dirty {
		aliases = normalised
		_ = WriteAllAliases(aliases)
	}

	return aliases, nil
}

func SaveAlias(shortcut string, alias Alias) error {
	aliases, err := LoadAliases()
	if err != nil {
		return err
	}

	aliases[shortcut] = alias
	path, err := GetConfigPath()
	if err != nil {
		return err
	}

	err = os.MkdirAll(filepath.Dir(path), 0755)
	if err != nil {
		return err
	}

	jsonData, err := json.MarshalIndent(aliases, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, jsonData, 0644)
}

func WriteAllAliases(aliases map[string]Alias) error {
	path, err := GetConfigPath()
	if err != nil {
		return err
	}

	err = os.MkdirAll(filepath.Dir(path), 0755)
	if err != nil {
		return err
	}

	jsonData, err := json.MarshalIndent(aliases, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, jsonData, 0644)
}
