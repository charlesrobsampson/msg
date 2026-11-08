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
// and exponential backoff. Durable queued work lives in the per-profile
// `forward_outbox` table, not in this type.
type Forwarder struct {
	URL         string
	Token       string
	Client      *http.Client
	MaxAttempts int
	BaseDelay   time.Duration
	Sleep       func(time.Duration)
	Logf        func(format string, args ...any)
	// Profile is the sending account/profile, used only for metric labels and
	// log correlation. Empty means "default".
	Profile string
	// Metrics receives attempt, duration, and drop observations. Nil disables
	// instrumentation (direct callers and tests).
	Metrics *Metrics
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

// inboundForwards is the receiver-wide, bounded per-profile forward queue. It
// is enqueued after the durable DB write, so the webhook ACK never waits on
// Aether.
var inboundForwards = NewForwardQueueWithMetrics(defaultForwardQueueDepth, forwardMetrics)

// errForwardURLMissing marks a profile that opted in but has no destination.
var errForwardURLMissing = errors.New("forwarding enabled but no url configured")

// resolveForwardTarget reads the per-account forwarding setting. The profile
// name is used for worker keying, metric labels, and log correlation; it is
// never the account itself.
func resolveForwardTarget(account string) (profile, url string, enabled bool, err error) {
	profile, lookupErr := client.FindProfileForAccount(account)
	if lookupErr != nil {
		profile = client.GetProfile()
	}
	target, err := client.ForwardingForAccount(account)
	if err != nil {
		return profile, "", false, err
	}
	if !target.Enabled {
		return profile, "", false, nil
	}
	url = strings.TrimSpace(target.URL)
	if url == "" {
		return profile, "", false, errForwardURLMissing
	}
	return profile, url, true, nil
}

// forwardJob is one queued delivery. The URL and token are resolved at enqueue
// time so a config toggle affects subsequent messages only. A non-nil store
// marks a durable outbox job whose row is deleted once the attempt finishes.
type forwardJob struct {
	url       string
	token     string
	msg       ForwardMessage
	store     *client.SignalDB
	messageID string
}

type forwardWorker struct {
	jobs chan forwardJob
}

// ForwardQueue is a bounded queue with one worker per profile, which preserves
// per-profile forward order. Enqueue never blocks; a saturated queue drops the
// newest job and increments Dropped.
type ForwardQueue struct {
	depth        int
	newForwarder func(url, token string) *Forwarder
	logf         func(format string, args ...any)
	metrics      *Metrics

	mu      sync.Mutex
	workers map[string]*forwardWorker
	closed  bool
	wg      sync.WaitGroup
	dropped atomic.Uint64
}

// NewForwardQueue returns a queue with the given per-profile depth. A
// non-positive depth selects the default (64).
func NewForwardQueue(depth int) *ForwardQueue {
	return NewForwardQueueWithMetrics(depth, forwardMetrics)
}

// NewForwardQueueWithMetrics returns a queue that reports to the given
// registry. Tests use this to keep registries isolated.
func NewForwardQueueWithMetrics(depth int, metrics *Metrics) *ForwardQueue {
	if depth <= 0 {
		depth = defaultForwardQueueDepth
	}
	if metrics == nil {
		metrics = forwardMetrics
	}
	return &ForwardQueue{
		depth:        depth,
		newForwarder: NewForwarder,
		metrics:      metrics,
		logf: func(format string, args ...any) {
			fmt.Printf("[forward] "+format+"\n", args...)
		},
		workers: make(map[string]*forwardWorker),
	}
}

// Enqueue offers one in-memory job without blocking. It returns false when the
// profile's buffer is full or the queue is closed. The profile name is used
// both as the worker key and as the metric label; callers must never pass an
// account or any other unbounded/PII value.
func (q *ForwardQueue) Enqueue(profile, url, token string, msg ForwardMessage) bool {
	return q.enqueue(profile, url, token, msg, nil, "")
}

// EnqueueDurable offers one job backed by a forward_outbox row. The row is
// deleted when the worker finishes; a closed or full queue leaves it pending for
// replay instead of losing it.
func (q *ForwardQueue) EnqueueDurable(store *client.SignalDB, profile, url, token string, msg ForwardMessage, messageID string) bool {
	return q.enqueue(profile, url, token, msg, store, messageID)
}

func (q *ForwardQueue) enqueue(profile, url, token string, msg ForwardMessage, store *client.SignalDB, messageID string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed {
		q.dropped.Add(1)
		q.metrics.RecordDropped(profile, DropShutdown)
		q.logf("forward queue closed for profile %s; deferring message %s", metricProfile(profile), msg.MessageID)
		return false
	}

	worker := q.workers[profile]
	if worker == nil {
		worker = &forwardWorker{jobs: make(chan forwardJob, q.depth)}
		q.workers[profile] = worker
		q.metrics.SetQueueDepth(profile, 0)
		q.wg.Add(1)
		go q.run(worker, profile)
	}

	job := forwardJob{url: url, token: token, msg: msg, store: store, messageID: messageID}
	select {
	case worker.jobs <- job:
		q.metrics.SetQueueDepth(profile, len(worker.jobs))
		return true
	default:
		q.dropped.Add(1)
		q.metrics.RecordDropped(profile, DropQueueFull)
		q.metrics.RecordAttempt(profile, AttemptQueueFull, 0)
		q.metrics.SetQueueDepth(profile, len(worker.jobs))
		q.logf("forward queue full for profile %s; deferring message %s", metricProfile(profile), msg.MessageID)
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

func (q *ForwardQueue) run(worker *forwardWorker, profile string) {
	defer q.wg.Done()
	for job := range worker.jobs {
		q.metrics.SetQueueDepth(profile, len(worker.jobs))
		forwarder := q.newForwarder(job.url, job.token)
		forwarder.Profile = profile
		forwarder.Metrics = q.metrics
		err := forwarder.Forward(context.Background(), job.msg)
		if job.store != nil {
			// The attempt is finished, successfully or after bounded retries, so
			// the durable row can go. A crash mid-attempt leaves it for replay.
			messageID := job.messageID
			if messageID == "" {
				messageID = job.msg.MessageID
			}
			if delErr := job.store.DeleteForward(messageID); delErr != nil {
				q.logf("forward outbox delete for profile %s message %s: %v", metricProfile(profile), messageID, delErr)
			}
			if pending, countErr := job.store.PendingForwardCount(); countErr == nil {
				q.metrics.SetOutboxPending(profile, pending)
			}
		}
		if err != nil {
			q.logf("forward failed for profile %s message %s: %v", metricProfile(profile), job.msg.MessageID, err)
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
		start := time.Now()
		lastErr = f.post(ctx, msg)
		elapsed := time.Since(start)
		if lastErr == nil {
			f.recordAttempt(AttemptOK, elapsed)
			return nil
		}

		retryable, retryAfter := shouldRetryForward(lastErr)
		if retryable {
			f.recordAttempt(AttemptRetryable, elapsed)
		} else {
			f.recordAttempt(AttemptTerminal, elapsed)
		}
		if !retryable || attempt == attempts {
			break
		}

		wait := delay
		if retryAfter > wait {
			wait = retryAfter
		}
		f.logf("attempt %d/%d for profile %s message %s to %s failed: %v; retrying in %s",
			attempt, attempts, f.profileLabel(), msg.MessageID, f.URL, lastErr, wait)
		f.nap(wait)
		delay = nextForwardDelay(delay)
	}

	f.recordDropped(DropTerminal)
	f.logf("delivery to %s for profile %s message %s failed after %d attempt(s): %v",
		f.URL, f.profileLabel(), msg.MessageID, made, lastErr)
	return lastErr
}

// profileLabel keeps the metric label set bounded and non-empty.
func (f *Forwarder) profileLabel() string {
	if f == nil || strings.TrimSpace(f.Profile) == "" {
		return "default"
	}
	return f.Profile
}

func (f *Forwarder) recordAttempt(result string, d time.Duration) {
	if f == nil || f.Metrics == nil {
		return
	}
	f.Metrics.RecordAttempt(f.profileLabel(), result, d)
}

func (f *Forwarder) recordDropped(reason string) {
	if f == nil || f.Metrics == nil {
		return
	}
	f.Metrics.RecordDropped(f.profileLabel(), reason)
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
