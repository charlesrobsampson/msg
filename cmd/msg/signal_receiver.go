package main

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charlesrobsampson/msg/client"
)

type signalAttachment struct {
	ContentType string `json:"contentType"`
	Filename    string `json:"filename"`
	ID          string `json:"id"`
	Size        int64  `json:"size"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
}

type signalReactionMessage struct {
	Emoji               string `json:"emoji"`
	Remove              bool   `json:"remove"`
	TargetSentTimestamp int64  `json:"targetSentTimestamp"`
	TargetAuthorNumber  string `json:"targetAuthorNumber"`
}

type SignalWebhookEnvelope struct {
	Source       string `json:"source"`
	SourceNumber string `json:"sourceNumber"`
	SourceUUID   string `json:"sourceUuid"`
	SourceName   string `json:"sourceName"`
	Timestamp    int64  `json:"timestamp"`
	DataMessage *struct {
		Message         string                  `json:"message"`
		Timestamp       int64                   `json:"timestamp"`
		Attachments     []signalAttachment      `json:"attachments"`
		ReactionMessage *signalReactionMessage  `json:"reaction"`
		GroupInfo       *struct {
			GroupID string `json:"groupId"`
		} `json:"groupInfo"`
	} `json:"dataMessage"`
	SyncMessage *struct {
		SentMessage *struct {
			Destination     string                 `json:"destination"`
			Timestamp       int64                  `json:"timestamp"`
			Message         string                 `json:"message"`
			Attachments     []signalAttachment     `json:"attachments"`
			ReactionMessage *signalReactionMessage `json:"reaction"`
			GroupInfo       *struct {
				GroupID string `json:"groupId"`
			} `json:"groupInfo"`
		} `json:"sentMessage"`
	} `json:"syncMessage"`
}

type SignalWebhookPayload struct {
	Envelope SignalWebhookEnvelope `json:"envelope"`
	Account  string               `json:"account"`
}

// SignalJSONRPCWrapper handles the json-rpc mode webhook format:
// {"jsonrpc":"2.0","method":"receive","params":{<SignalWebhookPayload>}}
type SignalJSONRPCWrapper struct {
	JSONRPC string               `json:"jsonrpc"`
	Method  string               `json:"method"`
	Params  *SignalWebhookPayload `json:"params"`
}

// Per-account DB cache so a single receiver process can serve all accounts.
var (
	dbCacheMu sync.Mutex
	dbByAcct  = make(map[string]*client.SignalDB)
)

func dbForAccount(account string) *client.SignalDB {
	dbCacheMu.Lock()
	defer dbCacheMu.Unlock()
	if db, ok := dbByAcct[account]; ok {
		return db
	}
	profile, err := client.FindProfileForAccount(account)
	if err != nil {
		// Unknown account — fall back to whatever profile this process was started with.
		profile = client.GetProfile()
	}
	db, openErr := client.OpenSignalDBForProfile(profile)
	if openErr != nil {
		fmt.Printf("[receiver] error opening DB for account %s (profile %q): %v\n", account, profile, openErr)
		return nil
	}
	dbByAcct[account] = db
	return db
}

func handleSignalReceiver() {
	port := 7008
	if envPort := os.Getenv("MSG_SIGNAL_RECEIVER_PORT"); envPort != "" {
		if p, err := strconv.Atoi(envPort); err == nil {
			port = p
		}
	}
	addr := fmt.Sprintf("0.0.0.0:%d", port)

	// Determine Signal API base URL and account from config so we can
	// maintain the WebSocket subscription that activates message delivery.
	cfg, _ := client.LoadConfig()
	signalBaseURL := ""
	signalAccount := cfg.Account
	for _, p := range cfg.Providers {
		if p.Type == "signal" && p.Enabled {
			if p.URL != "" {
				signalBaseURL = p.URL
			} else {
				sigPort := p.Port
				if sigPort == 0 {
					sigPort = 18081
				}
				signalBaseURL = fmt.Sprintf("http://127.0.0.1:%d", sigPort)
			}
		}
	}
	if signalAccount == "" {
		signalAccount = os.Getenv("SIGNAL_ACCOUNT")
	}

	// The WebSocket subscriber goroutine keeps a persistent connection to
	// /v1/receive/{account}.  Without an active subscriber the container
	// never tells signal-cli to start delivering, so RECEIVE_WEBHOOK_URL
	// stays silent.  We subscribe for every account registered in the
	// container so all profiles receive their messages.
	if signalBaseURL != "" {
		go startAllAccountSubscribers(signalBaseURL, signalAccount)
	}

	// Catch-all logger helps diagnose connectivity issues.
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Printf("[receiver] %s %s (no handler)\n", r.Method, r.URL.Path)
		http.NotFound(w, r)
	})

	http.HandleFunc("/api/signal/webhook", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			fmt.Printf("[receiver] unexpected method %s on webhook\n", r.Method)
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Error reading body", http.StatusInternalServerError)
			return
		}

		fmt.Printf("[receiver] webhook payload (%d bytes): %s\n", len(body), string(body))

		payloads := parseSignalPayloads(body)
		if payloads == nil {
			fmt.Printf("[receiver] JSON parse error: unrecognised payload format\n")
			http.Error(w, "Error parsing JSON", http.StatusBadRequest)
			return
		}

		for _, p := range payloads {
			saveSignalPayload(p)
		}

		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "OK")
	})

	fmt.Printf("Signal webhook receiver listening on %s...\n", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		fmt.Printf("Receiver failed: %v\n", err)
	}
}

// parseSignalPayloads handles both plain and json-rpc wrapped payload formats.
func parseSignalPayloads(body []byte) []SignalWebhookPayload {
	// json-rpc mode: {"jsonrpc":"2.0","method":"receive","params":{...}}
	var rpc SignalJSONRPCWrapper
	if json.Unmarshal(body, &rpc) == nil && rpc.Method == "receive" && rpc.Params != nil {
		return []SignalWebhookPayload{*rpc.Params}
	}

	// Plain array format (test mocks, REST mode)
	var arr []SignalWebhookPayload
	if json.Unmarshal(body, &arr) == nil {
		return arr
	}

	// Plain single-object format
	var single SignalWebhookPayload
	if json.Unmarshal(body, &single) == nil {
		return []SignalWebhookPayload{single}
	}

	return nil
}

// normalizeGroupID ensures group conversation IDs always use the "group." prefix
// so they match the IDs returned by the Signal API's /v1/groups endpoint.
func normalizeGroupID(id string) string {
	if strings.HasPrefix(id, "group.") {
		return id
	}
	return "group." + id
}

// saveSignalPayload extracts and persists a message from a webhook/WebSocket payload.
func saveSignalPayload(p SignalWebhookPayload) {
	db := dbForAccount(p.Account)
	if db == nil {
		return
	}

	var msg client.Message
	var convID string
	var attachments []client.RawAttachment

	// Reaction messages update an existing message — they don't create a new unread.
	// Use UpdateReactionInAllDBs so profiles whose config has no Account field
	// (and therefore can't be mapped via FindProfileForAccount) still get updated.
	if p.Envelope.DataMessage != nil && p.Envelope.DataMessage.ReactionMessage != nil {
		rxn := p.Envelope.DataMessage.ReactionMessage
		source := p.Envelope.Source
		if source == "" {
			source = p.Envelope.SourceNumber
		}
		convID = source
		if p.Envelope.DataMessage.GroupInfo != nil && p.Envelope.DataMessage.GroupInfo.GroupID != "" {
			convID = normalizeGroupID(p.Envelope.DataMessage.GroupInfo.GroupID)
		}
		senderName := p.Envelope.SourceName
		if senderName == "" {
			senderName = source
		}
		client.UpdateReactionInAllDBs(convID, rxn.TargetSentTimestamp, rxn.Emoji, senderName, rxn.Remove)
		fmt.Printf("[receiver] reaction %s on ts=%d in conv %s\n", rxn.Emoji, rxn.TargetSentTimestamp, convID)
		return
	}

	if p.Envelope.DataMessage != nil &&
		(p.Envelope.DataMessage.Message != "" || len(p.Envelope.DataMessage.Attachments) > 0) {
		source := p.Envelope.Source
		if source == "" {
			source = p.Envelope.SourceNumber
		}
		if source == "" {
			source = p.Envelope.SourceUUID
		}
		convID = source
		if p.Envelope.DataMessage.GroupInfo != nil && p.Envelope.DataMessage.GroupInfo.GroupID != "" {
			convID = normalizeGroupID(p.Envelope.DataMessage.GroupInfo.GroupID)
		}

		ts := p.Envelope.DataMessage.Timestamp
		if ts == 0 {
			ts = p.Envelope.Timestamp
		}
		if ts == 0 {
			ts = time.Now().UnixMilli()
		}

		for _, a := range p.Envelope.DataMessage.Attachments {
			attachments = append(attachments, client.RawAttachment{ID: a.ID, MimeType: a.ContentType})
		}

		msg = client.Message{
			MessageID:    fmt.Sprintf("rcv-%d-%s", ts, source),
			SenderName:   p.Envelope.SourceName,
			SenderNumber: source, // resolved: Source → SourceNumber → SourceUUID
			Body:         p.Envelope.DataMessage.Message,
			IsFromMe:     false,
			TimestampMS:  ts,
		}
	} else if p.Envelope.SyncMessage != nil && p.Envelope.SyncMessage.SentMessage != nil {
		sent := p.Envelope.SyncMessage.SentMessage

		// Reaction synced from own device — update the target message, no new unread.
		if sent.ReactionMessage != nil {
			rxn := sent.ReactionMessage
			convID = sent.Destination
			if sent.GroupInfo != nil && sent.GroupInfo.GroupID != "" {
				convID = normalizeGroupID(sent.GroupInfo.GroupID)
			}
			client.UpdateReactionInAllDBs(convID, rxn.TargetSentTimestamp, rxn.Emoji, "Me", rxn.Remove)
			fmt.Printf("[receiver] sync reaction %s on ts=%d in conv %s\n", rxn.Emoji, rxn.TargetSentTimestamp, convID)
			return
		}

		convID = sent.Destination
		if sent.GroupInfo != nil && sent.GroupInfo.GroupID != "" {
			convID = normalizeGroupID(sent.GroupInfo.GroupID)
		}

		ts := sent.Timestamp
		if ts == 0 {
			ts = p.Envelope.Timestamp
		}
		if ts == 0 {
			ts = time.Now().UnixMilli()
		}

		for _, a := range sent.Attachments {
			attachments = append(attachments, client.RawAttachment{ID: a.ID, MimeType: a.ContentType})
		}

		msg = client.Message{
			MessageID:    fmt.Sprintf("sync-%d", ts),
			SenderName:   "Me",
			SenderNumber: p.Account,
			Body:         sent.Message,
			IsFromMe:     true,
			TimestampMS:  ts,
		}
	} else {
		return
	}

	if err := db.SaveMessage(msg, convID, attachments); err != nil {
		fmt.Printf("[receiver] error saving message: %v\n", err)
	} else {
		typeStr := "incoming"
		if msg.IsFromMe {
			typeStr = "sync-sent"
		}
		fmt.Printf("[receiver] saved %s message for conv %s (account %s)\n", typeStr, convID, p.Account)
	}
}

// startAllAccountSubscribers queries /v1/accounts on the Signal API and starts
// a WebSocket subscriber goroutine for each registered account.  This ensures
// the container activates message delivery for all profiles, not just the one
// that started the receiver process.  Falls back to seedAccount if the API is
// unavailable or returns an empty list.
func startAllAccountSubscribers(baseURL, seedAccount string) {
	accounts := fetchAllSignalAccounts(baseURL)
	if len(accounts) == 0 && seedAccount != "" {
		accounts = []string{seedAccount}
	}
	seen := make(map[string]bool)
	for _, acct := range accounts {
		if seen[acct] {
			continue
		}
		seen[acct] = true
		go runWebSocketSubscriber(baseURL, acct)
	}
}

// fetchAllSignalAccounts calls GET /v1/accounts and returns the list of registered accounts.
func fetchAllSignalAccounts(baseURL string) []string {
	resp, err := http.Get(fmt.Sprintf("%s/v1/accounts", baseURL))
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	var accounts []string
	if err := json.NewDecoder(resp.Body).Decode(&accounts); err != nil {
		return nil
	}
	return accounts
}

// runWebSocketSubscriber maintains a persistent WebSocket connection to the
// Signal API /v1/receive/{account} endpoint.  Its presence activates signal-cli's
// message subscription; each received frame is also saved to DB so the app
// does not depend solely on the push webhook.
func runWebSocketSubscriber(baseURL, account string) {
	wsURL := strings.Replace(baseURL, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
	wsURL = fmt.Sprintf("%s/v1/receive/%s", wsURL, url.PathEscape(account))

	for {
		if err := wsReadLoop(wsURL); err != nil {
			fmt.Printf("[receiver] websocket: %v — reconnecting in 5s\n", err)
		}
		time.Sleep(5 * time.Second)
	}
}

func wsReadLoop(wsURL string) error {
	u, err := url.Parse(wsURL)
	if err != nil {
		return fmt.Errorf("parse URL: %v", err)
	}

	host := u.Host
	if !strings.Contains(host, ":") {
		if u.Scheme == "wss" {
			host += ":443"
		} else {
			host += ":80"
		}
	}

	conn, err := net.DialTimeout("tcp", host, 10*time.Second)
	if err != nil {
		return fmt.Errorf("dial %s: %v", host, err)
	}
	defer conn.Close()

	// WebSocket opening handshake.
	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		return fmt.Errorf("rand: %v", err)
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)

	upgradeReq := fmt.Sprintf(
		"GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n",
		u.RequestURI(), u.Host, key,
	)
	if _, err := conn.Write([]byte(upgradeReq)); err != nil {
		return fmt.Errorf("write upgrade: %v", err)
	}

	br := bufio.NewReader(conn)
	statusLine, err := br.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read status: %v", err)
	}
	if !strings.Contains(statusLine, "101") {
		return fmt.Errorf("upgrade rejected: %s", strings.TrimSpace(statusLine))
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return fmt.Errorf("read headers: %v", err)
		}
		if strings.TrimSpace(line) == "" {
			break
		}
	}

	fmt.Printf("[receiver] websocket connected — signal-cli subscription active\n")

	// Frame read loop.
	for {
		conn.SetReadDeadline(time.Now().Add(90 * time.Second))

		b0, err := br.ReadByte()
		if err != nil {
			return fmt.Errorf("frame header: %v", err)
		}
		b1, err := br.ReadByte()
		if err != nil {
			return fmt.Errorf("frame header: %v", err)
		}

		opcode := b0 & 0x0F
		payloadLen := int64(b1 & 0x7F)

		switch payloadLen {
		case 126:
			var l [2]byte
			if _, err := io.ReadFull(br, l[:]); err != nil {
				return err
			}
			payloadLen = int64(binary.BigEndian.Uint16(l[:]))
		case 127:
			var l [8]byte
			if _, err := io.ReadFull(br, l[:]); err != nil {
				return err
			}
			payloadLen = int64(binary.BigEndian.Uint64(l[:]))
		}

		// Server frames must not be masked (RFC 6455 §5.1).
		payload := make([]byte, payloadLen)
		if _, err := io.ReadFull(br, payload); err != nil {
			return fmt.Errorf("read payload: %v", err)
		}

		switch opcode {
		case 0x8: // close
			return fmt.Errorf("server sent close frame")
		case 0x9: // ping — respond with pong
			wsSendPong(conn, payload)
		case 0x1, 0x2: // text or binary — a Signal message
			fmt.Printf("[receiver] websocket frame (%d bytes): %s\n", len(payload), string(payload))
			payloads := parseSignalPayloads(payload)
			for _, p := range payloads {
				saveSignalPayload(p)
			}
		}
	}
}

// wsSendPong sends a masked pong frame (clients must mask per RFC 6455 §5.1).
func wsSendPong(conn net.Conn, payload []byte) {
	frame := make([]byte, 2+4+len(payload))
	frame[0] = 0x8A // FIN=1, opcode=0xA (pong)
	frame[1] = 0x80 | byte(len(payload))
	var mask [4]byte
	rand.Read(mask[:])
	copy(frame[2:6], mask[:])
	for i, b := range payload {
		frame[6+i] = b ^ mask[i%4]
	}
	conn.Write(frame)
}
