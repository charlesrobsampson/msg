package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charlesrobsampson/msg/client"
)

func main() {
	// Parse --profile / -p flag early
	profile := ""
	for i := 1; i < len(os.Args); i++ {
		if (os.Args[i] == "--profile" || os.Args[i] == "-p") && i+1 < len(os.Args) {
			profile = os.Args[i+1]
			client.SetProfile(profile)
			// Remove flag and its value from os.Args for further processing
			os.Args = append(os.Args[:i], os.Args[i+2:]...)
			i--
		}
	}

	// Load config and initialize providers
	cfg, err := client.LoadConfig()
	if err != nil {
		fmt.Printf("Warning: %v. Using defaults.\n", err)
	}
	client.InitProviders(cfg)

	// Check enabled providers
	hasOpenMessage := false
	var signalSettings *client.ProviderSettings

	for _, p := range cfg.Providers {
		if p.Enabled {
			if p.Type == "sms" || p.Type == "openmessage" {
				hasOpenMessage = true
			} else if p.Type == "signal" {
				s := p // copy
				signalSettings = &s
			}
		}
	}

	if len(os.Args) < 2 {
		runTUI()
		return
	}

	// Messaging commands require at least one enabled provider.
	messagingCmds := map[string]bool{"list": true, "unread": true, "search": true, "read": true, "send": true, "contacts": true, "quick-send": true}
	if messagingCmds[os.Args[1]] && isFirstRun() {
		fmt.Println("No providers are configured yet.")
		fmt.Println()
		fmt.Println("Enable a provider first:")
		fmt.Println("  msg provider enable google    then  msg pair google")
		fmt.Println("  msg provider enable signal    then  msg link signal")
		fmt.Println()
		fmt.Println("Or open the TUI ('msg') and go to Providers (tab 6) to set up interactively.")
		return
	}

	switch os.Args[1] {
	case "list":
		if hasHelpFlag(os.Args[2:]) {
			printCommandHelp("list")
			return
		}
		if f := unknownFlag(os.Args[2:], "--limit", "-l"); f != "" {
			fmt.Fprintf(os.Stderr, "unknown flag: %s\n\n", f)
			printCommandHelp("list")
			return
		}
		limit := 20
		for i := 2; i < len(os.Args); i++ {
			if (os.Args[i] == "--limit" || os.Args[i] == "-l") && i+1 < len(os.Args) {
				if l, err := strconv.Atoi(os.Args[i+1]); err == nil {
					limit = l
				}
				i++
			}
		}
		handleList(limit)

	case "search":
		if hasHelpFlag(os.Args[2:]) {
			printCommandHelp("search")
			return
		}
		if f := unknownFlag(os.Args[2:], "--limit", "-l"); f != "" {
			fmt.Fprintf(os.Stderr, "unknown flag: %s\n\n", f)
			printCommandHelp("search")
			return
		}
		if len(os.Args) < 3 {
			fmt.Println("Error: Missing search query.")
			return
		}
		limit := 20
		var queryParts []string
		for i := 2; i < len(os.Args); i++ {
			if (os.Args[i] == "--limit" || os.Args[i] == "-l") && i+1 < len(os.Args) {
				if l, err := strconv.Atoi(os.Args[i+1]); err == nil {
					limit = l
				}
				i++
			} else {
				queryParts = append(queryParts, os.Args[i])
			}
		}
		if len(queryParts) == 0 {
			fmt.Println("Error: Missing search query.")
			return
		}
		handleSearch(strings.Join(queryParts, " "), limit)

	case "read":
		if hasHelpFlag(os.Args[2:]) {
			printCommandHelp("read")
			return
		}
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "Error: missing conversation id or alias.")
			fmt.Fprintln(os.Stderr)
			printCommandHelp("read")
			return
		}
		if f := unknownFlag(os.Args[3:], "--limit", "-l", "--leave-unread", "-u", "--before", "--before-id"); f != "" {
			fmt.Fprintf(os.Stderr, "unknown flag: %s\n\n", f)
			printCommandHelp("read")
			return
		}
		id := resolveConvID(os.Args[2])

		limit := 100
		leaveUnread := false
		var beforeTS int64 = 0
		var beforeID string = ""

		for i := 3; i < len(os.Args); i++ {
			switch os.Args[i] {
			case "--limit", "-l":
				if i+1 < len(os.Args) {
					if l, err := strconv.Atoi(os.Args[i+1]); err == nil {
						limit = l
					}
					i++
				}
			case "--leave-unread", "-u":
				leaveUnread = true
			case "--before":
				if i+1 < len(os.Args) {
					if ts, err := strconv.ParseInt(os.Args[i+1], 10, 64); err == nil {
						beforeTS = ts
					}
					i++
				}
			case "--before-id":
				if i+1 < len(os.Args) {
					beforeID = os.Args[i+1]
					i++
				}
			}
		}
		handleRead(id, limit, leaveUnread, beforeTS, beforeID)

	case "send":
		if hasHelpFlag(os.Args[2:]) {
			printCommandHelp("send")
			return
		}
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "Error: missing conversation id/alias or message body.")
			fmt.Fprintln(os.Stderr)
			printCommandHelp("send")
			return
		}
		if f := unknownFlag(os.Args[4:], "--send", "-s", "--styled", "--style", "--attach", "-a"); f != "" {
			fmt.Fprintf(os.Stderr, "unknown flag: %s\n\n", f)
			printCommandHelp("send")
			return
		}

		targetInput := os.Args[2]
		messageBody := os.Args[3]
		shouldCommitSend := false
		styled := false
		var attachPaths []string

		for i := 4; i < len(os.Args); i++ {
			switch os.Args[i] {
			case "--send", "-s":
				shouldCommitSend = true
			case "--styled", "--style":
				styled = true
			case "--attach", "-a":
				if i+1 < len(os.Args) {
					i++
					for _, p := range strings.Split(os.Args[i], ",") {
						p = strings.TrimSpace(p)
						if p != "" {
							attachPaths = append(attachPaths, p)
						}
					}
				}
			}
		}

		// Validate attachment paths before sending.
		for _, p := range attachPaths {
			if _, err := os.Stat(p); os.IsNotExist(err) {
				fmt.Fprintf(os.Stderr, "attachment not found: %s\n", p)
				return
			}
		}

		handleAdvancedSend(targetInput, messageBody, shouldCommitSend, styled, attachPaths)

	case "unread":
		if hasHelpFlag(os.Args[2:]) {
			printCommandHelp("unread")
			return
		}
		if f := unknownFlag(os.Args[2:], "--count", "-c"); f != "" {
			fmt.Fprintf(os.Stderr, "unknown flag: %s\n\n", f)
			printCommandHelp("unread")
			return
		}
		countOnly := false
		for _, arg := range os.Args[2:] {
			if arg == "--count" || arg == "-c" {
				countOnly = true
			}
		}
		handleUnread(countOnly)

	case "contacts":
		if hasHelpFlag(os.Args[2:]) {
			printCommandHelp("contacts")
			return
		}
		if f := unknownFlag(os.Args[2:], "--limit", "-l"); f != "" {
			fmt.Fprintf(os.Stderr, "unknown flag: %s\n\n", f)
			printCommandHelp("contacts")
			return
		}
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "Error: missing search query.")
			fmt.Fprintln(os.Stderr)
			printCommandHelp("contacts")
			return
		}

		limit := 100 // Safe default upper bound for matching search results
		var queryParts []string

		for i := 2; i < len(os.Args); i++ {
			if os.Args[i] == "--limit" || os.Args[i] == "-l" {
				if i+1 < len(os.Args) {
					if l, err := strconv.Atoi(os.Args[i+1]); err == nil {
						limit = l
					}
					i++
				}
			} else {
				queryParts = append(queryParts, os.Args[i])
			}
		}

		query := strings.Join(queryParts, " ")
		if query == "" {
			fmt.Println("Error: Missing contact search query.")
			return
		}
		handleContacts(query, limit)

	case "quick-send":
		if hasHelpFlag(os.Args[2:]) {
			printCommandHelp("quick-send")
			return
		}
		if f := unknownFlag(os.Args[2:], "--send", "-s"); f != "" {
			fmt.Fprintf(os.Stderr, "unknown flag: %s\n\n", f)
			printCommandHelp("quick-send")
			return
		}
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "Error: missing contact name or message body.")
			fmt.Fprintln(os.Stderr)
			printCommandHelp("quick-send")
			return
		}

		shouldSend := false
		var cleanArgs []string

		for i := 2; i < len(os.Args); i++ {
			if os.Args[i] == "--send" || os.Args[i] == "-s" {
				shouldSend = true
			} else {
				cleanArgs = append(cleanArgs, os.Args[i])
			}
		}

		if len(cleanArgs) < 2 {
			fmt.Fprintln(os.Stderr, "Error: missing contact name or message body.")
			fmt.Fprintln(os.Stderr)
			printCommandHelp("quick-send")
			return
		}

		// The first remaining arg is the contact name, the second is the message body
		contactName := cleanArgs[0]
		messageBody := cleanArgs[1]
		handleQuickSend(contactName, messageBody, shouldSend)
	case "alias":
		if hasHelpFlag(os.Args[2:]) {
			printCommandHelp("alias")
			return
		}
		if len(os.Args) < 5 {
			fmt.Fprintln(os.Stderr, "Error: missing arguments.")
			fmt.Fprintln(os.Stderr)
			printCommandHelp("alias")
			return
		}
		shortcut := os.Args[2]
		target := os.Args[3]
		platform := os.Args[4]
		handleCreateAlias(shortcut, target, platform)

	case "link":
		if hasHelpFlag(os.Args[2:]) {
			printCommandHelp("link")
			return
		}
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "Error: missing provider.")
			fmt.Fprintln(os.Stderr)
			printCommandHelp("link")
			return
		}
		if os.Args[2] == "signal" {
			handleLinkSignal()
		}

	case "provider":
		if hasHelpFlag(os.Args[2:]) {
			printCommandHelp("provider")
			return
		}
		subCmd := ""
		if len(os.Args) >= 3 {
			subCmd = os.Args[2]
		}
		switch subCmd {
		case "status":
			handleProviderStatus()
		case "reconnect":
			if len(os.Args) < 4 {
				fmt.Fprintln(os.Stderr, "Usage: msg provider reconnect <google>")
				return
			}
			handleProviderReconnect(os.Args[3])
		case "disconnect":
			if len(os.Args) < 4 {
				fmt.Fprintln(os.Stderr, "Usage: msg provider disconnect <google>")
				return
			}
			handleProviderDisconnect(os.Args[3])
		case "enable":
			if len(os.Args) < 4 {
				fmt.Fprintln(os.Stderr, "Usage: msg provider enable <google|signal>")
				return
			}
			handleProviderToggle(os.Args[3], true)
		case "disable":
			if len(os.Args) < 4 {
				fmt.Println("Usage: msg provider disable <google|signal>")
				return
			}
			handleProviderToggle(os.Args[3], false)
		default:
			fmt.Println("Usage: msg provider <status|reconnect|disconnect|enable|disable>")
		}

	case "config":
		if hasHelpFlag(os.Args[2:]) {
			printCommandHelp("config")
			return
		}
		handleConfigCmd()

	case "pair":
		if hasHelpFlag(os.Args[2:]) {
			printCommandHelp("pair")
			return
		}
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "Error: missing provider.")
			fmt.Fprintln(os.Stderr)
			printCommandHelp("pair")
			return
		}
		handlePairProvider(os.Args[2])

	case "signal-receiver", "receive":
		handleSignalReceiver()

	case "server":
		if hasHelpFlag(os.Args[2:]) {
			printCommandHelp("server")
			return
		}
		subCmd := ""
		if len(os.Args) >= 3 {
			subCmd = os.Args[2]
		}
		switch subCmd {
		case "start":
			serverStart(cfg, hasOpenMessage, signalSettings)
		case "stop":
			serverStop(signalSettings)
		case "status":
			serverStatus(hasOpenMessage, signalSettings)
		default:
			fmt.Fprintln(os.Stderr, "Usage: msg server <start|stop|status>")
		}

	default:
		printHelp()
	}
}

// omServerBinaryPath returns the canonical location of the om-server binary:
// ~/.config/msg/om-server. This is install-location-independent.
func omServerBinaryPath() (string, error) {
	dir, err := client.ConfigDirPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "om-server"), nil
}

// buildOmServer tries to compile om-server from the submodule source into
// the canonical config-dir location. Only works when run from the repo root.
// Returns the binary path on success.
func buildOmServer() (string, error) {
	destPath, err := omServerBinaryPath()
	if err != nil {
		return "", err
	}

	// Source is only present when running from the cloned repo root.
	srcDir := filepath.Join("internal", "openmessage")
	if _, err := os.Stat(srcDir); os.IsNotExist(err) {
		return "", fmt.Errorf("source not found at %s — run 'msg server start' from the cloned repository root once to build it", srcDir)
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return "", err
	}

	build := exec.Command("go", "build", "-o", destPath, ".")
	build.Dir = srcDir
	if out, err := build.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build failed: %v\n%s", err, out)
	}
	os.Chmod(destPath, 0755)
	return destPath, nil
}

// ensureServerRunning starts the OpenMessage server if not already running.
// It is intentionally silent — callers that want output must print their own messages.
func ensureServerRunning() {
	port := 7007
	if envPort := os.Getenv("OPENMESSAGES_PORT"); envPort != "" {
		if p, err := strconv.Atoi(envPort); err == nil {
			port = p
		}
	}

	addr := fmt.Sprintf("localhost:%d", port)
	if conn, err := net.DialTimeout("tcp", addr, 1*time.Second); err == nil {
		conn.Close()
		return
	}

	binaryPath, err := omServerBinaryPath()
	if err != nil {
		return
	}

	// Build into the config dir if not already present.
	if _, err := os.Stat(binaryPath); os.IsNotExist(err) {
		if built, err := buildOmServer(); err != nil {
			fmt.Println("Error: om-server not found and could not be built.")
			fmt.Println("  Run 'msg server start' once from the cloned repository root to compile it.")
			return
		} else {
			binaryPath = built
		}
	}

	cfgDir, _ := client.ConfigDirPath()
	logPath := filepath.Join(cfgDir, "om-server.log")
	cmd := exec.Command("/bin/sh", "-c", fmt.Sprintf("PORT=%d OPENMESSAGES_HOST=0.0.0.0 nohup %s serve > %s 2>&1 &", port, binaryPath, logPath))
	if err := cmd.Start(); err != nil {
		return
	}
	cmd.Process.Release()

	for range 10 {
		time.Sleep(500 * time.Millisecond)
		if conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond); err == nil {
			conn.Close()
			return
		}
	}
}

// ensureSignalRunning starts the Signal CLI container if not already running.
// Returns true if the server is reachable after the call.
func ensureSignalRunning(port int) bool {
	if port == 0 {
		port = 18081
	}

	addr := fmt.Sprintf("localhost:%d", port)
	if conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond); err == nil {
		conn.Close()
		return true
	}

	home, _ := os.UserHomeDir()
	composePath := filepath.Join(home, ".config", "msg", "docker-compose-signal.yml")
	if _, err := os.Stat(composePath); os.IsNotExist(err) {
		if err := writeSignalSetupFiles(); err != nil {
			fmt.Printf("Error: Signal setup files missing and could not be created: %v\n", err)
			return false
		}
		fmt.Println("Signal setup files written to ~/.config/msg/")
		fmt.Println("Building Signal container (this takes ~2 minutes on first run)...")
		build := exec.Command("docker", "compose", "-f", composePath, "build")
		build.Stdout = os.Stdout
		build.Stderr = os.Stderr
		if err := build.Run(); err != nil {
			fmt.Printf("Error building Signal container: %v\n", err)
			return false
		}
	}

	cmd := exec.Command("docker", "compose", "-f", composePath, "up", "-d")
	if err := cmd.Run(); err != nil {
		fmt.Printf("Error starting Signal container: %v\n", err)
		return false
	}

	for range 20 {
		time.Sleep(500 * time.Millisecond)
		if conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond); err == nil {
			conn.Close()
			return true
		}
	}
	return false
}

// ensureSignalReceiverRunning starts the msg signal-receiver process if not already running.
// It is intentionally silent — callers that want output must print their own messages.
func ensureSignalReceiverRunning() {
	port := 7008
	if envPort := os.Getenv("MSG_SIGNAL_RECEIVER_PORT"); envPort != "" {
		if p, err := strconv.Atoi(envPort); err == nil {
			port = p
		}
	}
	addr := fmt.Sprintf("localhost:%d", port)

	if conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond); err == nil {
		conn.Close()
		return
	}

	home, _ := os.UserHomeDir()
	logName := "signal-receiver.log"
	profileArg := ""
	if p := client.GetProfile(); p != "" {
		logName = "signal-receiver-" + p + ".log"
		profileArg = "--profile " + p + " "
	}
	logPath := filepath.Join(home, ".config", "msg", logName)

	cmd := exec.Command("/bin/sh", "-c", fmt.Sprintf("MSG_SIGNAL_RECEIVER_PORT=%d nohup %s %ssignal-receiver > %s 2>&1 &", port, os.Args[0], profileArg, logPath))
	if err := cmd.Start(); err != nil {
		return
	}
	cmd.Process.Release()

	for range 10 {
		time.Sleep(500 * time.Millisecond)
		if conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond); err == nil {
			conn.Close()
			return
		}
	}
}

func serverStart(cfg client.Config, hasOpenMessage bool, signalSettings *client.ProviderSettings) {
	if hasOpenMessage {
		fmt.Println("Starting OpenMessage server...")
		ensureServerRunning()
		fmt.Println("OpenMessage server ready.")
	}
	if signalSettings != nil {
		fmt.Printf("Starting Signal server (port %d)...\n", signalSettings.Port)
		if ensureSignalRunning(signalSettings.Port) {
			fmt.Println("Signal server ready.")
			fmt.Println("Starting Signal message receiver...")
			ensureSignalReceiverRunning()
			fmt.Println("Signal receiver ready.")
		} else {
			fmt.Println("Signal server did not start. Run 'msg server status' to check.")
		}
	}
	fmt.Println("All services started.")
}

func serverStop(signalSettings *client.ProviderSettings) {
	stopOpenMessageServer()
	fmt.Println("OpenMessage server stopped.")

	stopSignalReceiver()
	fmt.Println("Signal receiver stopped.")

	if signalSettings != nil {
		stopSignalServer()
		fmt.Println("Signal server stopped.")
	}
}

func serverStatus(hasOpenMessage bool, signalSettings *client.ProviderSettings) {
	if hasOpenMessage {
		addr := fmt.Sprintf("localhost:%d", 7007)
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			fmt.Println("[✓] OpenMessage server  running on :7007")
		} else {
			fmt.Println("[✗] OpenMessage server  not running")
		}
	}

	if signalSettings != nil {
		port := signalSettings.Port
		if port == 0 {
			port = 18081
		}
		addr := fmt.Sprintf("localhost:%d", port)
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			fmt.Printf("[✓] Signal API          running on :%d\n", port)
		} else {
			fmt.Printf("[✗] Signal API          not running (port %d)\n", port)
		}

		receiverPort := 7008
		if envPort := os.Getenv("MSG_SIGNAL_RECEIVER_PORT"); envPort != "" {
			if p, err := strconv.Atoi(envPort); err == nil {
				receiverPort = p
			}
		}
		rAddr := fmt.Sprintf("localhost:%d", receiverPort)
		conn, err = net.DialTimeout("tcp", rAddr, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			fmt.Printf("[✓] Signal receiver     running on :%d\n", receiverPort)
		} else {
			fmt.Printf("[✗] Signal receiver     not running (port %d)\n", receiverPort)
		}
	}
}

func handleConfigCmd() {
	cfg, err := client.LoadConfig()
	if err != nil {
		fmt.Printf("Error loading config: %v\n", err)
		return
	}

	if len(os.Args) < 3 {
		fmt.Printf("editor: %s\n", cfg.Editor)
		fmt.Println()
		fmt.Println("Usage: msg config set <key> <value>")
		fmt.Println("  Keys: editor")
		return
	}

	if os.Args[2] != "set" || len(os.Args) < 5 {
		fmt.Println("Usage: msg config set <key> <value>")
		fmt.Println("  Keys: editor")
		return
	}

	key, value := os.Args[3], os.Args[4]
	switch key {
	case "editor":
		cfg.Editor = value
		if err := client.SaveConfig(cfg); err != nil {
			fmt.Printf("Error saving config: %v\n", err)
			return
		}
		fmt.Printf("editor set to %q\n", value)
	default:
		fmt.Printf("Unknown config key %q. Available keys: editor\n", key)
	}
}

func hasHelpFlag(args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return true
		}
	}
	return false
}

// unknownFlag returns the first arg in args that looks like a flag (starts with
// '-' but is not a digit) and is not in the valid set or the built-in -h/--help.
func unknownFlag(args []string, valid ...string) string {
	validSet := map[string]bool{"-h": true, "--help": true}
	for _, v := range valid {
		validSet[v] = true
	}
	for _, arg := range args {
		if len(arg) < 2 || arg[0] != '-' {
			continue
		}
		if arg[1] >= '0' && arg[1] <= '9' {
			continue // negative number, not a flag
		}
		if !validSet[arg] {
			return arg
		}
	}
	return ""
}

func printCommandHelp(cmd string) {
	switch cmd {
	case "list":
		fmt.Println("Usage: msg list [-l <n>]")
		fmt.Println()
		fmt.Println("List all tracked conversations, sorted by most recent activity.")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  -l, --limit <n>    Max conversations to return (default: 20)")
		fmt.Println("  -h, --help         Show this help")

	case "search":
		fmt.Println("Usage: msg search <query> [-l <n>]")
		fmt.Println()
		fmt.Println("Search conversations by name.")
		fmt.Println()
		fmt.Println("Arguments:")
		fmt.Println("  query              Text to search for")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  -l, --limit <n>    Max results (default: 20)")
		fmt.Println("  -h, --help         Show this help")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  msg search alice")
		fmt.Println(`  msg search "project team" -l 5`)

	case "read":
		fmt.Println("Usage: msg read <id|alias> [-l <n>] [-u] [--before <ts>] [--before-id <id>]")
		fmt.Println()
		fmt.Println("Read messages in a conversation. Marks it as read unless -u is set.")
		fmt.Println()
		fmt.Println("Arguments:")
		fmt.Println("  id|alias               Conversation ID or alias shortcut")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  -l, --limit <n>        Max messages to fetch (default: 100)")
		fmt.Println("  -u, --leave-unread     Don't mark conversation as read")
		fmt.Println("      --before <ts>      Fetch messages before this Unix ms timestamp")
		fmt.Println("      --before-id <id>   Fetch messages before this message ID")
		fmt.Println("  -h, --help             Show this help")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  msg read sms:192")
		fmt.Println("  msg read alice -l 20")
		fmt.Println("  msg read cbot -u")

	case "send":
		fmt.Println("Usage: msg send <id|alias> <body> [-s] [-a paths] [--styled]")
		fmt.Println()
		fmt.Println("Send a message. Without -s, prints a dry-run preview.")
		fmt.Println()
		fmt.Println("Arguments:")
		fmt.Println("  id|alias              Conversation ID or alias shortcut")
		fmt.Println("  body                  Message text")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  -s, --send            Commit and transmit the message")
		fmt.Println("  -a, --attach <paths>  Comma-separated file paths to attach")
		fmt.Println("      --styled          Send with provider-native text formatting")
		fmt.Println("  -h, --help            Show this help")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println(`  msg send cbot "hey"`)
		fmt.Println(`  msg send cbot "hey" -s`)
		fmt.Println(`  msg send cbot "see attached" -s -a photo.jpg,doc.pdf`)

	case "unread":
		fmt.Println("Usage: msg unread [-c]")
		fmt.Println()
		fmt.Println("Show all unread messages across all conversations.")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  -c, --count        Print only the total unread count")
		fmt.Println("  -h, --help         Show this help")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  msg unread")
		fmt.Println("  msg unread -c")

	case "contacts":
		fmt.Println("Usage: msg contacts <query> [-l <n>]")
		fmt.Println()
		fmt.Println("Search the contact address book.")
		fmt.Println()
		fmt.Println("Arguments:")
		fmt.Println("  query              Name or phone number to search for")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  -l, --limit <n>    Max results (default: 100)")
		fmt.Println("  -h, --help         Show this help")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  msg contacts alice")
		fmt.Println(`  msg contacts "+1" -l 10`)

	case "quick-send":
		fmt.Println("Usage: msg quick-send <name> <body> [-s]")
		fmt.Println()
		fmt.Println("Look up a contact by name or alias and send a message.")
		fmt.Println()
		fmt.Println("Arguments:")
		fmt.Println("  name               Contact name or alias shortcut")
		fmt.Println("  body               Message text")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  -s, --send         Commit and transmit the message")
		fmt.Println("  -h, --help         Show this help")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println(`  msg quick-send alice "hello"`)
		fmt.Println(`  msg quick-send alice "hello" -s`)

	case "alias":
		fmt.Println("Usage: msg alias <shortcut> <conv_id> <platform>")
		fmt.Println()
		fmt.Println("Create a shortcut for a conversation ID used in send/read.")
		fmt.Println()
		fmt.Println("Arguments:")
		fmt.Println("  shortcut           Short name to use in other commands")
		fmt.Println("  conv_id            Full conversation ID (e.g. signal:+15551234567)")
		fmt.Println("  platform           Provider name (signal or sms)")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  -h, --help         Show this help")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  msg alias alice signal:+15551234567 signal")
		fmt.Println("  msg alias work sms:192 sms")

	case "link":
		fmt.Println("Usage: msg link <provider>")
		fmt.Println()
		fmt.Println("Link a new device to a provider.")
		fmt.Println()
		fmt.Println("Arguments:")
		fmt.Println("  provider           Provider to link (currently: signal)")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  -h, --help         Show this help")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  msg link signal")

	case "provider":
		fmt.Println("Usage: msg provider <subcommand> [args]")
		fmt.Println()
		fmt.Println("Manage provider connections.")
		fmt.Println()
		fmt.Println("Subcommands:")
		fmt.Println("  status                     Show status of all providers")
		fmt.Println("  enable <google|signal>     Enable a provider")
		fmt.Println("  disable <google|signal>    Disable a provider")
		fmt.Println("  reconnect google           Reconnect Google Messages (soft reconnect)")
		fmt.Println("  disconnect google          Disconnect and unpair Google Messages")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  -h, --help         Show this help")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  msg provider status")
		fmt.Println("  msg provider enable signal")
		fmt.Println("  msg provider reconnect google")

	case "pair":
		fmt.Println("Usage: msg pair <provider>")
		fmt.Println()
		fmt.Println("Re-register a provider via QR code.")
		fmt.Println()
		fmt.Println("Arguments:")
		fmt.Println("  provider           Provider to pair (currently: google)")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  -h, --help         Show this help")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  msg pair google")

	case "server":
		fmt.Println("Usage: msg server <subcommand>")
		fmt.Println()
		fmt.Println("Control backend services.")
		fmt.Println()
		fmt.Println("Subcommands:")
		fmt.Println("  start    Start all backend services")
		fmt.Println("  stop     Stop all backend services")
		fmt.Println("  status   Show service health")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  -h, --help         Show this help")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  msg server start")
		fmt.Println("  msg server status")

	case "config":
		fmt.Println("Usage: msg config [set <key> <value>]")
		fmt.Println()
		fmt.Println("View or update configuration settings.")
		fmt.Println()
		fmt.Println("Subcommands:")
		fmt.Println("  (none)             Show current config values")
		fmt.Println("  set editor <path>  Set the editor command (e.g. vim, nvim, nano)")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  -h, --help         Show this help")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  msg config")
		fmt.Println("  msg config set editor nvim")

	default:
		printHelp()
	}
}

func printHelp() {
	fmt.Println("Usage: msg [-p <profile>] <command> [flags]")
	fmt.Println()
	fmt.Println("Global flags:")
	fmt.Println("  -p, --profile <name>    Use a named config profile")
	fmt.Println()
	fmt.Println("Server lifecycle:")
	fmt.Println("  server start            Start all backend services")
	fmt.Println("  server stop             Stop all backend services")
	fmt.Println("  server status           Show service health")
	fmt.Println()
	fmt.Println("Messaging:")
	fmt.Println("  list [-l <n>]                                   List conversations")
	fmt.Println("  unread [-c]                                     Show unread messages")
	fmt.Println("  search <query> [-l <n>]                         Search conversations")
	fmt.Println("  read <id|alias> [-l <n>] [-u] [--before <ts>]  Read a conversation")
	fmt.Println("  send <id|alias> <body> [-s] [-a path1,path2]   Send a message (dry-run without -s)")
	fmt.Println("  contacts <query> [-l <n>]                       Search contacts")
	fmt.Println("  quick-send <name> <body> [-s]                   Look up contact and send")
	fmt.Println("  alias <shortcut> <conv_id> <platform>           Create a send alias")
	fmt.Println("  link signal                                     Link a Signal device")
	fmt.Println()
	fmt.Println("Flags:")
	fmt.Println("  -l, --limit <n>         Max results to return")
	fmt.Println("  -u, --leave-unread      Don't mark conversation as read after reading")
	fmt.Println("  -c, --count             Print only the count (for unread)")
	fmt.Println("  -s, --send              Commit and send (omit for dry-run)")
	fmt.Println("  -a, --attach <paths>    Comma-separated file paths to attach")
	fmt.Println("      --before <ts>       Fetch messages before this Unix ms timestamp")
	fmt.Println("      --before-id <id>    Fetch messages before this message ID")
	fmt.Println()
	fmt.Println("Provider management:")
	fmt.Println("  provider status                   Show status of all providers")
	fmt.Println("  provider reconnect google         Reconnect Google Messages (soft reconnect)")
	fmt.Println("  provider disconnect google        Disconnect and unpair Google Messages")
	fmt.Println("  pair google                       Re-register Google Messages via QR code")
	fmt.Println()
	fmt.Println("Configuration:")
	fmt.Println("  config                            Show current config values")
	fmt.Println("  config set editor <editor>        Set the editor (e.g. vim, nvim, nano)")
}

func handleProviderStatus() {
	providers := client.GetProviders()
	found := false
	for _, p := range providers {
		if omp, ok := p.(*client.OpenMessageProvider); ok {
			found = true
			status, err := omp.GetStatus()
			if err != nil {
				fmt.Printf("Google Messages (sms): UNREACHABLE — %v\n", err)
				continue
			}
			if status.Google != nil {
				g := status.Google
				switch {
				case g.NeedsPairing:
					msg := "Needs Pairing"
					if g.LastError != "" {
						msg += " (" + g.LastError + ")"
					}
					fmt.Printf("Google Messages: %s\n", msg)
					fmt.Println("  Run 'msg pair google' to re-register.")
				case !g.Connected:
					msg := "Disconnected"
					if g.LastError != "" {
						msg += " (" + g.LastError + ")"
					}
					fmt.Printf("Google Messages: %s\n", msg)
					fmt.Println("  Run 'msg provider reconnect google' to reconnect.")
				default:
					fmt.Println("Google Messages: Connected")
				}
			} else {
				if status.Connected {
					fmt.Println("OpenMessage: Connected")
				} else {
					fmt.Println("OpenMessage: Disconnected")
				}
			}
		}
	}
	if !found {
		fmt.Println("No OpenMessage provider configured.")
	}
}

// providerConfigKey maps user-facing provider names to config map keys.
func providerConfigKey(name string) (string, string) {
	switch name {
	case "google":
		return "sms", "Google Messages"
	case "signal":
		return "signal", "Signal"
	default:
		return "", ""
	}
}

func handleProviderToggle(provider string, enable bool) {
	key, displayName := providerConfigKey(provider)
	if key == "" {
		fmt.Printf("Unknown provider: %s (use 'google' or 'signal')\n", provider)
		return
	}
	cfg, err := client.LoadConfig()
	if err != nil {
		fmt.Printf("Error loading config: %v\n", err)
		return
	}
	if cfg.Providers == nil {
		cfg.Providers = make(map[string]client.ProviderSettings)
	}
	p := cfg.Providers[key]
	p.Type = key
	p.Enabled = enable
	cfg.Providers[key] = p
	if err := client.SaveConfig(cfg); err != nil {
		fmt.Printf("Error saving config: %v\n", err)
		return
	}
	if enable {
		fmt.Printf("%s enabled.\n", displayName)
		if key == "sms" {
			fmt.Println("Run 'msg pair google' to register Google Messages.")
		} else if key == "signal" {
			if err := writeSignalSetupFiles(); err != nil {
				fmt.Printf("Warning: could not write Signal setup files: %v\n", err)
			} else {
				fmt.Println("Signal setup files written to ~/.config/msg/")
				fmt.Println("Build the container:  docker compose -f ~/.config/msg/docker-compose-signal.yml build")
			}
			fmt.Println("Run 'msg link signal' to link a Signal device.")
		}
	} else {
		fmt.Printf("%s disabled.\n", displayName)
	}
}

func isFirstRun() bool {
	cfg, err := client.LoadConfig()
	if err != nil {
		return true
	}
	for _, p := range cfg.Providers {
		if p.Enabled {
			return false
		}
	}
	return true
}

func handleProviderDisconnect(provider string) {
	if provider != "google" {
		fmt.Printf("Unknown provider: %s\n", provider)
		return
	}
	providers := client.GetProviders()
	for _, p := range providers {
		if omp, ok := p.(*client.OpenMessageProvider); ok {
			fmt.Println("Disconnecting Google Messages...")
			if err := omp.Disconnect(); err != nil {
				fmt.Printf("Disconnect failed: %v\n", err)
			} else {
				fmt.Println("Google Messages disconnected. Run 'msg pair google' to re-register.")
			}
			return
		}
	}
	fmt.Println("Error: OpenMessage provider not configured.")
}

func handleProviderReconnect(provider string) {
	if provider != "google" {
		fmt.Printf("Unknown provider: %s\n", provider)
		return
	}
	providers := client.GetProviders()
	for _, p := range providers {
		if omp, ok := p.(*client.OpenMessageProvider); ok {
			fmt.Println("Attempting to reconnect Google Messages...")
			if err := omp.ReconnectGoogle(); err != nil {
				fmt.Printf("Reconnect failed: %v\n", err)
				fmt.Println("If credentials are expired, run 'msg pair google' to re-register.")
			} else {
				fmt.Println("Google Messages reconnected successfully.")
			}
			return
		}
	}
	fmt.Println("Error: OpenMessage provider not configured.")
}

func stopOpenMessageServer() {
	if out, err := exec.Command("pgrep", "-f", "om-server").Output(); err == nil {
		for _, pidStr := range strings.Fields(string(out)) {
			if pid, err := strconv.Atoi(pidStr); err == nil {
				if p, err := os.FindProcess(pid); err == nil {
					p.Kill()
				}
			}
		}
	}
}

func stopSignalServer() {
	home, _ := os.UserHomeDir()
	composePath := filepath.Join(home, ".config", "msg", "docker-compose-signal.yml")
	if _, err := os.Stat(composePath); err == nil {
		cmd := exec.Command("docker", "compose", "-f", composePath, "down")
		cmd.Run()
	}
}

func stopSignalReceiver() {
	if out, err := exec.Command("pgrep", "-f", "signal-receiver").Output(); err == nil {
		for _, pidStr := range strings.Fields(string(out)) {
			if pid, err := strconv.Atoi(pidStr); err == nil {
				if p, err := os.FindProcess(pid); err == nil {
					p.Kill()
				}
			}
		}
	}
}

func handlePairProvider(provider string) {
	if provider != "google" {
		fmt.Printf("Unknown provider: %s\n", provider)
		return
	}

	binaryPath, err := omServerBinaryPath()
	if err != nil {
		fmt.Printf("Error resolving om-server path: %v\n", err)
		return
	}
	if _, err := os.Stat(binaryPath); os.IsNotExist(err) {
		fmt.Println("Error: om-server binary not found. Run 'msg server start' first to build it.")
		return
	}

	fmt.Println("Stopping OpenMessage server for pairing...")
	stopOpenMessageServer()

	fmt.Println()
	fmt.Println("Starting Google Messages pairing — scan the QR code with your phone.")
	fmt.Println()

	// No flags = QR code mode. --google triggers cookie-based pairing instead.
	cmd := exec.Command(binaryPath, "pair")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	pairErr := cmd.Run()

	// Always restart the server so the system isn't left in a stopped state.
	fmt.Println("\nRestarting OpenMessage server...")
	ensureServerRunning()

	if pairErr != nil {
		fmt.Printf("Pairing exited with error: %v\n", pairErr)
		fmt.Println("If pairing did not complete, run 'msg pair google' again.")
	} else {
		fmt.Println("Pairing successful. Run 'msg provider status' to verify.")
	}
}

func handleList(limit int) {
	convs, err := client.FetchConversations(limit)
	if err != nil {
		fmt.Printf("Error fetching conversations: %v\n", err)
		return
	}

	fmt.Printf("%-6s | %-25s | %s\n", "ID", "Name", "Unread")
	fmt.Println(strings.Repeat("-", 45))
	for _, c := range convs {
		name := c.Name
		if name == "" {
			name = "Unknown"
		}
		fmt.Printf("%-6s | %-25s | %d\n", c.ConversationID, name, c.UnreadCount)
	}
}

func handleSearch(query string, limit int) {
	convs, err := client.SearchConversations(query, limit)
	if err != nil {
		fmt.Printf("Error searching conversations: %v\n", err)
		return
	}

	fmt.Printf("\n🔎 Conversations matching '%s':\n", query)
	fmt.Printf("%-6s | %-25s | %s\n", "ID", "Name", "Unread")
	fmt.Println(strings.Repeat("-", 45))
	for _, c := range convs {
		name := c.Name
		if name == "" {
			name = "Unknown"
		}
		fmt.Printf("%-6s | %-25s | %d\n", c.ConversationID, name, c.UnreadCount)
	}
}

func handleRead(id string, limit int, leaveUnread bool, beforeTS int64, beforeID string) {
	// FIX 1: Provide required cursor pagination values. Default variables provide safe initial page fetches.
	messages, err := client.FetchMessages(id, limit, beforeTS, beforeID)
	if err != nil {
		fmt.Printf("Error fetching messages: %v\n", err)
		return
	}

	isSignal := strings.HasPrefix(id, "signal:")
	fmt.Printf("\n--- Live Conversation Thread Logs (%s) ---\n", id)
	for _, m := range messages {
		sender := m.SenderName
		if m.IsFromMe {
			sender = "Me"
		} else if sender == "" {
			sender = "Them"
		}

		body := strings.TrimSpace(m.Body)
		if isSignal && body != "" {
			body = renderSignalBodyCLI(body)
		}
		t := time.UnixMilli(m.TimestampMS).Format("01/02 03:04 PM")
		fmt.Printf("[%s - %s]: %s\n", t, sender, body)
		for _, att := range m.Attachments {
			label := att.MimeType
			if label == "" {
				label = "attachment"
			}
			fmt.Printf("    📎 %s\n", label)
		}
	}

	if !leaveUnread {
		_ = client.MarkAsRead(id)
	}
}

type unreadEntry struct {
	msg  client.Message
	conv client.Conversation
}

func handleUnread(countOnly bool) {
	conversations, err := client.FetchConversations(30)
	if err != nil {
		fmt.Printf("Error fetching conversations: %v\n", err)
		return
	}

	aliases, _ := client.LoadAliases()

	// Build a lookup from prefixed conversationID → alias shortcut.
	aliasFor := make(map[string]string)
	for _, a := range aliases {
		aliasFor[a.ConversationID] = a.Shortcut
	}

	var entries []unreadEntry

	for _, c := range conversations {
		if c.UnreadCount > 0 {
			messages, err := client.FetchMessages(c.ConversationID, c.UnreadCount, 0, "")
			if err != nil {
				continue
			}

			unreadCounted := 0
			for _, msg := range messages {
				if unreadCounted >= c.UnreadCount {
					break
				}
				if !msg.IsFromMe {
					entries = append(entries, unreadEntry{msg: msg, conv: c})
					unreadCounted++
				}
			}
		}
	}

	if countOnly {
		fmt.Println(len(entries))
		return
	}

	if len(entries) == 0 {
		fmt.Println("🎉 Everything is caught up! No unread messages.")
		return
	}

	// Sort newest-first by message timestamp.
	for i := 0; i < len(entries)-1; i++ {
		for j := i + 1; j < len(entries); j++ {
			if entries[j].msg.TimestampMS > entries[i].msg.TimestampMS {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}

	fmt.Printf("\n📬 Unread Messages (Newest First):\n")
	fmt.Println(strings.Repeat("-", 60))
	for _, e := range entries {
		t := time.UnixMilli(e.msg.TimestampMS).Format("01/02 15:04")

		sender := e.msg.SenderName
		if sender == "" {
			sender = e.conv.Name
		}

		platform := e.conv.SourcePlatform
		if platform == "" {
			platform = e.conv.ProviderID
		}

		convID := e.conv.ConversationID
		alias := aliasFor[convID]

		fmt.Printf("[%s] %s | %s\n", t, sender, e.conv.Name)
		fmt.Printf("   Platform : %s\n", platform)
		fmt.Printf("   ID       : %s\n", convID)
		if alias != "" {
			fmt.Printf("   Alias    : %s\n", alias)
			fmt.Printf("   Reply    : msg send %s \"...\" --send\n", alias)
		} else {
			fmt.Printf("   Reply    : msg send %s \"...\" --send\n", convID)
		}
		body := e.msg.Body
		if e.conv.SourcePlatform == "signal" && body != "" {
			body = renderSignalBodyCLI(body)
		}
		fmt.Printf("   > %s\n\n", body)
	}
}

func handleContacts(query string, limit int) {
	contacts, err := client.FetchContacts(query, limit)
	if err != nil {
		fmt.Printf("Error fetching contacts: %v\n", err)
		return
	}

	if len(contacts) == 0 {
		fmt.Printf("\n👤 No contacts found matching: '%s'\n", query)
		return
	}

	fmt.Printf("\n👤 Address Book Lookup (Query: '%s', Results: %d)\n", query, len(contacts))
	fmt.Printf("%-10s | %-25s | %s\n", "ID", "Contact Target Identity", "Phone Number")
	fmt.Println(strings.Repeat("-", 55))
	for _, c := range contacts {
		name := c.Name
		if name == "" {
			name = "Unknown"
		}
		fmt.Printf("%-10s | %-25s | %s\n", c.ContactID, name, c.Number)
	}
}

func handleQuickSend(contactName, body string, shouldSend bool) {
	var convID string
	found := false

	// 1. Try alias lookup
	aliases, err := client.LoadAliases()
	if err == nil {
		for _, a := range aliases {
			if a.Shortcut == contactName {
				convID = a.ConversationID
				found = true
				break
			}
		}
	}

	// 2. Fallback to contact lookup
	if !found {
		contacts, err := client.FetchContacts(contactName, 5)
		if err != nil || len(contacts) == 0 {
			fmt.Printf("No contact or alias found matching '%s'\n", contactName)
			return
		}

		targetContact := contacts[0]
		convID, err = client.FindConversationByNumber(targetContact.Number, 100)
		if err != nil {
			fmt.Printf("Error resolving conversation: %v\n", err)
			return
		}
	}

	if !shouldSend {
		fmt.Println("\n--- DRY RUN (not sent) ---")
		fmt.Printf("To      : %s\n", convID)
		fmt.Printf("Message : %q\n", body)
		fmt.Printf("\nTo send: msg quick-send %s %q --send\n", contactName, body)
		return
	}

	fmt.Printf("Sending to conversation %s...\n", convID)
	success, err := client.SendMessage(convID, body, false)
	if err != nil || !success {
		fmt.Printf("Error sending message: %v\n", err)
		return
	}

	fmt.Println("Message sent.")
}

func handleCreateAlias(shortcut, target, platform string) {
	fmt.Printf("Creating alias mapping for '%s' on platform '%s'...\n", shortcut, platform)

	newAlias := client.Alias{
		Shortcut:       shortcut,
		ConversationID: target,
		Platform:       platform,
		Name:           "Manually Alias Linked",
	}

	if strings.HasPrefix(target, "+") || len(target) > 6 {
		contacts, err := client.FetchContacts(shortcut, 1)
		if err == nil && len(contacts) > 0 {
			newAlias.Name = contacts[0].Name
			newAlias.Number = contacts[0].Number
		}
	}

	err := client.SaveAlias(shortcut, newAlias)
	if err != nil {
		fmt.Printf("Error writing alias configurations: %v\n", err)
		return
	}

	fmt.Printf("Success! You can now use 'msg send %s \"body\"' to interact.\n", shortcut)
}

func handleLinkSignal() {
	providers := client.GetProviders()
	var signalProvider *client.SignalProvider
	for _, p := range providers {
		if sp, ok := p.(*client.SignalProvider); ok {
			signalProvider = sp
			break
		}
	}

	if signalProvider == nil {
		fmt.Println("Error: Signal provider is not enabled.")
		fmt.Println("Enable it first:  msg provider enable signal")
		return
	}

	fmt.Printf("Connecting to Signal API at %s ...\n", signalProvider.GetBaseURL())

	linkURL := signalProvider.GetBaseURL() + "/v1/qrcodelink/raw?device_name=msg-cli"
	resp, err := http.Get(linkURL)
	if err != nil {
		fmt.Printf("\nCannot reach Signal API: %v\n", err)
		fmt.Println("\nThe signal-cli REST API server must be running before you can link.")
		fmt.Println("Start it with:  msg server start")
		fmt.Println("Or configure server_cmd in your config and use [t] on the Platforms page.")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		fmt.Printf("\nSignal API returned HTTP %d: %s\n", resp.StatusCode, strings.TrimSpace(string(body)))
		return
	}

	var result struct {
		URI string `json:"device_link_uri"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || result.URI == "" {
		fmt.Printf("Error reading Signal API response: %v\n", err)
		return
	}

	fmt.Println("\n--- Signal Device Linking ---")
	fmt.Printf("Link URI:\n  %s\n\n", result.URI)

	// Try to render a QR code inline.
	qrCmd := exec.Command("qrencode", "-t", "UTF8", result.URI)
	if out, err := qrCmd.Output(); err == nil {
		fmt.Print(string(out))
	} else {
		fmt.Println("(Install 'qrencode' to display a QR code here: brew install qrencode)")
		fmt.Println("\nOr paste the URI above into any QR code generator.")
	}

	fmt.Println("\nOn your phone: Signal → Settings → Linked Devices → Link New Device")
	fmt.Println("Scan the QR code or paste the URI above.")
	fmt.Println("\nPress Enter once you have scanned, or Ctrl+C to cancel.")

	bufio.NewReader(os.Stdin).ReadString('\n')

	fmt.Println("Returning to msg...")
	time.Sleep(500 * time.Millisecond)
}

// resolveConvID returns the conversation ID for the given input, resolving
// alias shortcuts to their stored ConversationID when a match exists.
func resolveConvID(input string) string {
	aliases, err := client.LoadAliases()
	if err != nil {
		return input
	}
	for _, a := range aliases {
		if a.Shortcut == input {
			return a.ConversationID
		}
	}
	return input
}

func handleAdvancedSend(targetInput, body string, shouldCommitSend bool, styled bool, attachPaths []string) {
	resolvedID := ""
	recipientName := ""

	// 1. Resolve targetInput
	aliases, err := client.LoadAliases()
	if err == nil {
		for _, a := range aliases {
			if a.Shortcut == targetInput {
				resolvedID = a.ConversationID
				recipientName = a.Name
				break
			}
		}
	}

	// 2. Fallback: If not an alias, check if it's a raw Conversation ID
	if resolvedID == "" {
		conversations, err := client.FetchConversations(500)
		if err == nil {
			for _, c := range conversations {
				if c.ConversationID == targetInput {
					resolvedID = c.ConversationID
					recipientName = c.Name
					break
				}
			}
		}
	}

	// 3. Error if still not resolved
	if resolvedID == "" {
		fmt.Printf("Error: Could not resolve target '%s' to a valid conversation or alias.\n", targetInput)
		return
	}

	toLine := fmt.Sprintf("%s [%s]", recipientName, resolvedID)

	if !shouldCommitSend {
		fmt.Println("\n--- DRY RUN (not sent) ---")
		fmt.Printf("To      : %s\n", toLine)
		fmt.Printf("Message : %q\n", body)
		if styled {
			fmt.Println("Style   : on")
		}
		if len(attachPaths) > 0 {
			fmt.Printf("Attach  : %s\n", strings.Join(attachPaths, ", "))
		}
		styledFlag := ""
		if styled {
			styledFlag = " --styled"
		}
		attachFlag := ""
		if len(attachPaths) > 0 {
			attachFlag = " --attach " + strings.Join(attachPaths, ",")
		}
		fmt.Printf("\nTo send: msg send %s %q --send%s%s\n", targetInput, body, styledFlag, attachFlag)
		return
	}

	fmt.Printf("Sending to %s...\n", toLine)

	success, err := client.SendMessageWithAttachments(resolvedID, body, styled, attachPaths)
	if err != nil || !success {
		fmt.Printf("Error sending message: %v\n", err)
		return
	}

	fmt.Println("Message sent.")
}
