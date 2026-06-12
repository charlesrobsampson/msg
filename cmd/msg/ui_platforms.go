package main

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/charlesrobsampson/msg/client"
)

type platformStatus struct {
	name             string
	providerType     string // "google", "signal"
	configKey        string // key in cfg.Providers map
	state            string // "connected", "needs_pairing", "disconnected", "unreachable", "disabled"
	lastError        string
	account          string
	url              string // base URL for the provider's API/UI
	canReconnect     bool   // soft reconnect via API (no QR)
	canPair          bool   // full QR / link re-registration
	canDisconnect    bool   // unpair / remove credentials
	canToggleEnabled bool   // enable / disable this provider
	enabled          bool   // current enabled state
	canStart         bool   // server is not reachable and can be started (server_cmd or built-in)
	canStop          bool   // server is reachable and can be stopped
	serverPort       int    // port used to find the process when stopping without a PID
	serverCmd        string
	receiverRunning  bool // signal-receiver process is listening
	receiverPort     int
	canStartReceiver bool // Signal server is up but receiver is not running
}

// knownProviders lists all providers the app supports, in display order.
var knownProviders = []struct {
	configKey    string
	providerType string
	displayName  string
}{
	{"sms", "google", "Google Messages"},
	{"signal", "signal", "Signal"},
}

// loadPlatformStatuses queries each known provider for its current status,
// including disabled and unconfigured ones.
func loadPlatformStatuses(cfg client.Config) []platformStatus {
	var statuses []platformStatus

	for _, kp := range knownProviders {
		ps, configured := cfg.Providers[kp.configKey]
		if !configured || !ps.Enabled {
			st := platformStatus{
				name:             kp.displayName,
				providerType:     kp.providerType,
				configKey:        kp.configKey,
				state:            "disabled",
				canToggleEnabled: true,
				enabled:          false,
				serverCmd:        ps.ServerCmd,
			}
			// Even when disabled in config, allow starting the server if a command is set.
			if ps.ServerCmd != "" {
				st.canStart = true
			}
			statuses = append(statuses, st)
			continue
		}

		var st platformStatus
		switch kp.providerType {
		case "google":
			st = googleMessagesStatus(ps)
		case "signal":
			st = signalProviderStatus(ps, cfg.Account)
		}
		st.configKey = kp.configKey
		st.canToggleEnabled = true
		st.enabled = true
		st.serverCmd = ps.ServerCmd

		port := ps.Port
		if port == 0 {
			switch kp.providerType {
			case "google":
				port = 7007
			case "signal":
				port = 18081
			}
		}
		st.serverPort = port

		if ps.URL != "" {
			st.url = ps.URL
		} else {
			st.url = fmt.Sprintf("http://127.0.0.1:%d", port)
		}

		switch st.state {
		case "unreachable":
			// Can start if there's a configured command or if the built-in launcher supports this type.
			st.canStart = ps.ServerCmd != "" || kp.providerType == "google" || kp.providerType == "signal"
		default:
			// Server is reachable — offer stop.
			st.canStop = true
			// For Signal: also surface a receiver-start action when it's not running.
			if kp.providerType == "signal" && !st.receiverRunning {
				st.canStartReceiver = true
			}
		}

		statuses = append(statuses, st)
	}

	return statuses
}

func googleMessagesStatus(cfg client.ProviderSettings) platformStatus {
	ps := platformStatus{
		name:         "Google Messages",
		providerType: "google",
		canPair:      true,
	}
	prov := client.NewOpenMessageProvider("sms", cfg, "")
	status, err := prov.GetStatus()
	if err != nil {
		ps.state = "unreachable"
		ps.lastError = err.Error()
		return ps
	}
	if status.Google != nil {
		g := status.Google
		ps.lastError = g.LastError
		switch {
		case g.NeedsPairing:
			ps.state = "needs_pairing"
		case !g.Connected:
			ps.state = "disconnected"
			ps.canReconnect = true
			ps.canDisconnect = true
		default:
			ps.state = "connected"
			ps.canDisconnect = true
		}
	} else {
		if status.Connected {
			ps.state = "connected"
			ps.canDisconnect = true
		} else {
			ps.state = "disconnected"
			ps.canReconnect = true
			ps.canDisconnect = true
		}
	}
	return ps
}

func signalProviderStatus(cfg client.ProviderSettings, account string) platformStatus {
	ps := platformStatus{
		name:         "Signal",
		providerType: "signal",
		account:      account,
		canPair:      true,
	}

	port := cfg.Port
	if port == 0 {
		port = 18081
	}
	host := fmt.Sprintf("127.0.0.1:%d", port)
	if cfg.URL != "" {
		h := strings.TrimPrefix(cfg.URL, "https://")
		h = strings.TrimPrefix(h, "http://")
		host = h
	}

	conn, err := net.DialTimeout("tcp", host, 500*time.Millisecond)
	if err != nil {
		ps.state = "unreachable"
	} else {
		conn.Close()
		if account != "" {
			ps.state = "connected"
			ps.canDisconnect = true
		} else {
			ps.state = "needs_pairing"
		}
	}

	// Check whether the signal-receiver process is listening.
	receiverPort := 7008
	if envPort := os.Getenv("MSG_SIGNAL_RECEIVER_PORT"); envPort != "" {
		if p, err := strconv.Atoi(envPort); err == nil {
			receiverPort = p
		}
	}
	ps.receiverPort = receiverPort
	if rc, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", receiverPort), 300*time.Millisecond); err == nil {
		rc.Close()
		ps.receiverRunning = true
	}

	return ps
}

func stateLabel(state string) string {
	switch state {
	case "connected":
		return "[Connected]"
	case "needs_pairing":
		return "[Needs Pairing]"
	case "disconnected":
		return "[Disconnected]"
	case "unreachable":
		return "[Unreachable]"
	case "disabled":
		return "[Disabled]"
	default:
		return "[Unknown]"
	}
}

func (m tuiModel) renderPlatformsView() (string, string) {
	var left, right strings.Builder
	left.WriteString(activeStyle.Render("PROVIDERS") + "\n\n")

	statuses := m.platformStatuses
	if len(statuses) == 0 {
		left.WriteString(subtleStyle.Render("No providers configured.") + "\n")
		return left.String(), ""
	}

	for i, ps := range statuses {
		marker := " "
		if i == m.selectedIndex {
			marker = ">"
		}
		left.WriteString(fmt.Sprintf("%s %-20s %s\n", marker, ps.name, subtleStyle.Render(stateLabel(ps.state))))
	}

	// Right panel: details + available actions for the selected provider.
	if m.selectedIndex >= 0 && m.selectedIndex < len(statuses) {
		sel := statuses[m.selectedIndex]
		right.WriteString(activeStyle.Render("PROVIDER DETAILS") + "\n\n")
		right.WriteString(fmt.Sprintf("Name    : %s\n", sel.name))
		if sel.providerType == "google" {
			right.WriteString(subtleStyle.Render("          (provider ID: sms)") + "\n")
		}
		right.WriteString(fmt.Sprintf("Status  : %s\n", sel.state))
		if sel.account != "" {
			right.WriteString(fmt.Sprintf("Account : %s\n", sel.account))
		}
		if sel.url != "" {
			right.WriteString(fmt.Sprintf("URL     : %s\n", sel.url))
		}
		switch sel.providerType {
		case "google":
			right.WriteString(fmt.Sprintf("Docs    : %s\n", subtleStyle.Render("https://github.com/MaxGhenis/openmessage/tree/main")))
		case "signal":
			right.WriteString(fmt.Sprintf("Docs    : %s\n", subtleStyle.Render("https://bbernhard.github.io/signal-cli-rest-api/")))
		}
		if sel.lastError != "" {
			right.WriteString(fmt.Sprintf("Error   : %s\n", sel.lastError))
		}
		if sel.providerType == "signal" {
			if sel.receiverRunning {
				right.WriteString(fmt.Sprintf("Receiver: running (port %d)\n", sel.receiverPort))
			} else {
				right.WriteString(activeStyle.Render(fmt.Sprintf("Receiver: NOT RUNNING (port %d) — incoming messages disabled", sel.receiverPort)) + "\n")
			}
		}

		right.WriteString("\n")
		hasActions := sel.canReconnect || sel.canPair || sel.canDisconnect || sel.canToggleEnabled || sel.canStart || sel.canStop || sel.canStartReceiver
		if hasActions {
			right.WriteString(subtleStyle.Render("Actions:") + "\n")
			if sel.canStart {
				right.WriteString("  [t] Start server\n")
			}
			if sel.canStop {
				right.WriteString("  [t] Stop server\n")
			}
			if sel.canStartReceiver {
				right.WriteString(activeStyle.Render("  [w] Start message receiver") + "\n")
			}
			if sel.canReconnect {
				right.WriteString("  [r] Reconnect (soft, no QR needed)\n")
			}
			if sel.canPair {
				switch sel.providerType {
				case "google":
					right.WriteString("  [p] Re-register via QR code\n")
				case "signal":
					right.WriteString("  [p] Link device via QR code\n")
				}
			}
			if sel.canDisconnect {
				right.WriteString("  [d] Disconnect\n")
			}
			if sel.canToggleEnabled {
				if sel.enabled {
					right.WriteString("  [e] Disable provider\n")
				} else {
					right.WriteString("  [e] Enable provider\n")
				}
			}
		} else if sel.state == "connected" {
			right.WriteString(subtleStyle.Render("Provider is healthy.") + "\n")
		}

		if sel.serverCmd != "" {
			right.WriteString(fmt.Sprintf("\n%s\n", subtleStyle.Render("Server command:")))
			right.WriteString(fmt.Sprintf("  %s\n", sel.serverCmd))
		}
	}

	return left.String(), right.String()
}
