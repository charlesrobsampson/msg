package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charlesrobsampson/msg/client"
)

const (
	defaultForwardAttempts  = 3
	defaultForwardBaseDelay = 200 * time.Millisecond
	maxForwardDelay         = 30 * time.Second
	forwardHTTPTimeout      = 5 * time.Second
	// defaultForwardQueueDepth bounds one profile's pending forwards. A full
	// queue drops the newest message instead of blocking ingestion (A03).
	defaultForwardQueueDepth = 64
)

// ForwardMessage is the normalized inbound envelope POSTed to Aether's
// /receive endpoint. It mirrors plan.md §3.2.
type ForwardMessage struct {
	Sender    string `json:"sender"`
	Text      string `json:"text"`
	Provider  string `json:"provider,omitempty"`
	GroupID   string `json:"group_id,omitempty"`
	MessageID string `json:"message_id,omitempty"`
}

// Forwarder posts inbound messages to Aether with a bounded number of retries
// and exponential backoff. It never spools messages: msg's own database remains
// the replay source.
type Forwarder struct {
	URL         string
	Token       string
	Client      *http.Client
	MaxAttempts int
	BaseDelay   time.Duration
	Sleep       func(time.Duration)
	Logf        func(format string, args ...any)
}

// defaultForwardHTTPClient is shared by every forwarder so per-message
// forwarding does not allocate a new client for each inbound message.
var defaultForwardHTTPClient = &http.Client{Timeout: forwardHTTPTimeout}

// NewForwarder builds a forwarder. An empty URL means forwarding is disabled.
func NewForwarder(url, token string) *Forwarder {
	return &Forwarder{
		URL:         strings.TrimSpace(url),
		Token:       strings.TrimSpace(token),
		Client:      defaultForwardHTTPClient,
		MaxAttempts: defaultForwardAttempts,
		BaseDelay:   defaultForwardBaseDelay,
	}
}

// inboundForwards is the receiver-wide, bounded per-account forward queue. It
// is enqueued after the DB write, so the webhook ACK never waits on Aether.
var inboundForwards = NewForwardQueue(defaultForwardQueueDepth)

// forwardInbound hands one normalized message to the sending account's
// per-profile worker when that profile has opted in. The config is re-read for
// every message so forwarding can be toggled without restarting the receiver.
// Config errors and an enabled-but-empty URL are treated as "disabled for this
// message"; they are logged and never fail ingestion. Enqueue never blocks: a
// full queue drops the newest message and counts it.
func forwardInbound(ctx context.Context, account string, msg ForwardMessage) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return
		}
	}
	target, err := client.ForwardingForAccount(account)
	if err != nil {
		fmt.Printf("[receiver] forwarding config for account %s: %v\n", account, err)
		return
	}
	if !target.Enabled {
		return
	}
	url := strings.TrimSpace(target.URL)
	if url == "" {
		fmt.Printf("[receiver] forwarding enabled for account %s but no url is configured\n", account)
		return
	}

	inboundForwards.Enqueue(account, url, os.Getenv("AETHER_INGRESS_TOKEN"), msg)
}

// forwardJob is one queued delivery. The URL and token are resolved at enqueue
// time so a config toggle affects subsequent messages only.
type forwardJob struct {
	url   string
	token string
	msg   ForwardMessage
}

type forwardWorker struct {
	jobs chan forwardJob
}

// ForwardQueue is a bounded queue with one worker per account (and therefore
// per profile), which preserves per-profile forward order. Enqueue never
// blocks; a saturated queue drops the newest job and increments Dropped.
type ForwardQueue struct {
	depth        int
	newForwarder func(url, token string) *Forwarder
	logf         func(format string, args ...any)

	mu      sync.Mutex
	workers map[string]*forwardWorker
	closed  bool
	wg      sync.WaitGroup
	dropped atomic.Uint64
}

// NewForwardQueue returns a queue with the given per-profile depth. A
// non-positive depth selects the default (64).
func NewForwardQueue(depth int) *ForwardQueue {
	if depth <= 0 {
		depth = defaultForwardQueueDepth
	}
	return &ForwardQueue{
		depth:        depth,
		newForwarder: NewForwarder,
		logf: func(format string, args ...any) {
			fmt.Printf("[forward] "+format+"\n", args...)
		},
		workers: make(map[string]*forwardWorker),
	}
}

// Enqueue offers one job without blocking. It returns false when the profile's
// buffer is full or the queue is closed.
func (q *ForwardQueue) Enqueue(account, url, token string, msg ForwardMessage) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed {
		q.dropped.Add(1)
		q.logf("forward queue closed for account %s; dropping message %s", account, msg.MessageID)
		return false
	}

	worker := q.workers[account]
	if worker == nil {
		worker = &forwardWorker{jobs: make(chan forwardJob, q.depth)}
		q.workers[account] = worker
		q.wg.Add(1)
		go q.run(worker)
	}

	select {
	case worker.jobs <- forwardJob{url: url, token: token, msg: msg}:
		return true
	default:
		q.dropped.Add(1)
		q.logf("forward queue full for account %s; dropping message %s", account, msg.MessageID)
		return false
	}
}

// Dropped reports how many forwards were dropped without an attempt.
func (q *ForwardQueue) Dropped() uint64 {
	return q.dropped.Load()
}

// Close stops accepting jobs and waits for in-flight and buffered forwards to
// finish. It is idempotent.
func (q *ForwardQueue) Close() {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.closed = true
	for _, worker := range q.workers {
		close(worker.jobs)
	}
	q.mu.Unlock()

	q.wg.Wait()
}

func (q *ForwardQueue) run(worker *forwardWorker) {
	defer q.wg.Done()
	for job := range worker.jobs {
		forwarder := q.newForwarder(job.url, job.token)
		if err := forwarder.Forward(context.Background(), job.msg); err != nil {
			q.logf("forward failed for message %s: %v", job.msg.MessageID, err)
		}
	}
}

// Enabled reports whether a destination URL was configured.
func (f *Forwarder) Enabled() bool {
	return f != nil && strings.TrimSpace(f.URL) != ""
}

// Forward delivers one message, retrying transient failures up to MaxAttempts
// times. It returns the last error after giving up; callers in the receiver log
// and ignore it so a broken Aether never fails message ingestion.
func (f *Forwarder) Forward(ctx context.Context, msg ForwardMessage) error {
	if !f.Enabled() {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	attempts := f.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}
	delay := f.BaseDelay
	if delay < 0 {
		delay = 0
	}

	var lastErr error
	made := 0
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		made = attempt
		lastErr = f.post(ctx, msg)
		if lastErr == nil {
			return nil
		}

		retryable, retryAfter := shouldRetryForward(lastErr)
		if !retryable || attempt == attempts {
			break
		}

		wait := delay
		if retryAfter > wait {
			wait = retryAfter
		}
		f.logf("attempt %d/%d to %s failed: %v; retrying in %s", attempt, attempts, f.URL, lastErr, wait)
		f.nap(wait)
		delay = nextForwardDelay(delay)
	}

	f.logf("delivery to %s failed after %d attempt(s): %v", f.URL, made, lastErr)
	return lastErr
}

// shouldRetryForward implements the shared retry classification: only network
// errors, 429, and 5xx are transient. Every other 4xx is terminal.
func shouldRetryForward(err error) (bool, time.Duration) {
	var statusErr *client.StatusError
	if errors.As(err, &statusErr) {
		if statusErr.StatusCode == http.StatusTooManyRequests ||
			(statusErr.StatusCode >= 500 && statusErr.StatusCode < 600) {
			return true, statusErr.RetryAfter
		}
		return false, 0
	}
	if errors.Is(err, context.Canceled) {
		return false, 0
	}
	var netErr net.Error
	if errors.As(err, &netErr) || errors.Is(err, context.DeadlineExceeded) {
		return true, 0
	}
	return false, 0
}

func (f *Forwarder) post(ctx context.Context, msg ForwardMessage) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal inbound message: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.URL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build forward request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if f.Token != "" {
		req.Header.Set("X-Aether-Token", f.Token)
	}

	resp, err := f.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("post to %s: %w", f.URL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &client.StatusError{
			Provider:   "aether",
			StatusCode: resp.StatusCode,
			RetryAfter: client.ParseRetryAfter(resp.Header.Get("Retry-After")),
			Body:       strings.TrimSpace(string(detail)),
		}
	}

	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil
}

func (f *Forwarder) httpClient() *http.Client {
	if f.Client != nil {
		return f.Client
	}
	return &http.Client{Timeout: forwardHTTPTimeout}
}

func (f *Forwarder) nap(d time.Duration) {
	if d <= 0 {
		return
	}
	if f.Sleep != nil {
		f.Sleep(d)
		return
	}
	time.Sleep(d)
}

func (f *Forwarder) logf(format string, args ...any) {
	if f.Logf != nil {
		f.Logf(format, args...)
		return
	}
	fmt.Printf("[forward] "+format+"\n", args...)
}

func nextForwardDelay(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	d *= 2
	if d > maxForwardDelay {
		return maxForwardDelay
	}
	return d
}
