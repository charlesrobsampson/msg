package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Metric names exposed on the loopback metrics listener (plan §3.19).
const (
	forwardAttemptsName   = "msg_forward_attempts_total"
	forwardDurationName   = "msg_forward_duration_seconds"
	forwardQueueDepthName = "msg_forward_queue_depth"
	forwardDroppedName    = "msg_forward_dropped_total"
)

// Bounded forward outcome labels. Keeping them as constants prevents
// high-cardinality labels such as phone numbers or message IDs.
const (
	AttemptOK        = "ok"
	AttemptRetryable = "retryable"
	AttemptTerminal  = "terminal"
	AttemptQueueFull = "queue_full"

	DropQueueFull = "queue_full"
	DropTerminal  = "terminal"
	DropShutdown  = "shutdown"
)

// attemptKey is a (profile, result) pair for attempts and durations.
type attemptKey struct {
	profile string
	result  string
}

// dropKey is a (profile, reason) pair for dropped forwards.
type dropKey struct {
	profile string
	reason  string
}

// durationSample accumulates one summary series. Only sum and count are
// exposed; quantiles stay a Prometheus-server concern (plan §3.19).
type durationSample struct {
	sum   float64
	count uint64
}

// Metrics is the msg-side Prometheus registry. It is safe for concurrent use.
type Metrics struct {
	mu        sync.Mutex
	attempts  map[attemptKey]uint64
	durations map[attemptKey]*durationSample
	depths    map[string]int
	dropped   map[dropKey]uint64
}

// NewMetrics returns an empty registry.
func NewMetrics() *Metrics {
	return &Metrics{
		attempts:  make(map[attemptKey]uint64),
		durations: make(map[attemptKey]*durationSample),
		depths:    make(map[string]int),
		dropped:   make(map[dropKey]uint64),
	}
}

// forwardMetrics is the receiver-wide registry used by the global forward
// queue and served on the loopback metrics listener.
var forwardMetrics = NewMetrics()

func metricProfile(profile string) string {
	if strings.TrimSpace(profile) == "" {
		return "default"
	}
	return profile
}

// RecordAttempt counts one forward POST attempt and records its latency. A
// non-positive duration records the attempt without a latency sample, which is
// how an enqueue-time queue_full drop is reported.
func (m *Metrics) RecordAttempt(profile, result string, d time.Duration) {
	if m == nil {
		return
	}
	key := attemptKey{profile: metricProfile(profile), result: result}

	m.mu.Lock()
	m.attempts[key]++
	if d > 0 {
		sample := m.durations[key]
		if sample == nil {
			sample = &durationSample{}
			m.durations[key] = sample
		}
		sample.sum += d.Seconds()
		sample.count++
	}
	m.mu.Unlock()
}

// RecordDropped counts one forward dropped without a successful delivery.
func (m *Metrics) RecordDropped(profile, reason string) {
	if m == nil {
		return
	}
	key := dropKey{profile: metricProfile(profile), reason: reason}
	m.mu.Lock()
	m.dropped[key]++
	m.mu.Unlock()
}

// SetQueueDepth records the current occupancy of a profile's forward queue.
func (m *Metrics) SetQueueDepth(profile string, depth int) {
	if m == nil {
		return
	}
	if depth < 0 {
		depth = 0
	}
	m.mu.Lock()
	m.depths[metricProfile(profile)] = depth
	m.mu.Unlock()
}

// Attempts reports the attempt count for one profile/result pair.
func (m *Metrics) Attempts(profile, result string) uint64 {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.attempts[attemptKey{profile: metricProfile(profile), result: result}]
}

// Dropped reports dropped forwards for one profile/reason pair.
func (m *Metrics) Dropped(profile, reason string) uint64 {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dropped[dropKey{profile: metricProfile(profile), reason: reason}]
}

// QueueDepth reports the last recorded depth for a profile.
func (m *Metrics) QueueDepth(profile string) int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.depths[metricProfile(profile)]
}

// Duration reports the sum and count for one profile/result pair.
func (m *Metrics) Duration(profile, result string) (float64, uint64) {
	if m == nil {
		return 0, 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	sample := m.durations[attemptKey{profile: metricProfile(profile), result: result}]
	if sample == nil {
		return 0, 0
	}
	return sample.sum, sample.count
}

// Handler serves the registry in Prometheus text exposition format.
func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if m == nil {
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
			return
		}

		var b strings.Builder
		m.writeExposition(&b)

		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(b.String()))
	})
}

func (m *Metrics) writeExposition(b *strings.Builder) {
	m.mu.Lock()
	attempts := make(map[attemptKey]uint64, len(m.attempts))
	for key, value := range m.attempts {
		attempts[key] = value
	}
	durations := make(map[attemptKey]durationSample, len(m.durations))
	for key, value := range m.durations {
		durations[key] = *value
	}
	depths := make(map[string]int, len(m.depths))
	for profile, depth := range m.depths {
		depths[profile] = depth
	}
	dropped := make(map[dropKey]uint64, len(m.dropped))
	for key, value := range m.dropped {
		dropped[key] = value
	}
	m.mu.Unlock()

	fmt.Fprintf(b, "# HELP %s Total forward POST attempts by profile and result.\n", forwardAttemptsName)
	fmt.Fprintf(b, "# TYPE %s counter\n", forwardAttemptsName)
	for _, key := range sortedAttemptKeys(attempts) {
		fmt.Fprintf(b, "%s{profile=%q,result=%q} %d\n",
			forwardAttemptsName, key.profile, key.result, attempts[key])
	}

	fmt.Fprintf(b, "# HELP %s Forward POST latency in seconds by profile and result.\n", forwardDurationName)
	fmt.Fprintf(b, "# TYPE %s summary\n", forwardDurationName)
	for _, key := range sortedAttemptKeys(durations) {
		base := fmt.Sprintf("%s{profile=%q,result=%q}",
			forwardDurationName, key.profile, key.result)
		sample := durations[key]
		fmt.Fprintf(b, "%s_sum %s\n", base, strconv.FormatFloat(sample.sum, 'g', -1, 64))
		fmt.Fprintf(b, "%s_count %d\n", base, sample.count)
	}

	fmt.Fprintf(b, "# HELP %s Pending forwards per profile.\n", forwardQueueDepthName)
	fmt.Fprintf(b, "# TYPE %s gauge\n", forwardQueueDepthName)
	profiles := make([]string, 0, len(depths))
	for profile := range depths {
		profiles = append(profiles, profile)
	}
	sort.Strings(profiles)
	for _, profile := range profiles {
		fmt.Fprintf(b, "%s{profile=%q} %d\n", forwardQueueDepthName, profile, depths[profile])
	}

	fmt.Fprintf(b, "# HELP %s Forwards dropped without successful delivery by profile and reason.\n", forwardDroppedName)
	fmt.Fprintf(b, "# TYPE %s counter\n", forwardDroppedName)
	droppedKeys := make([]dropKey, 0, len(dropped))
	for key := range dropped {
		droppedKeys = append(droppedKeys, key)
	}
	sort.Slice(droppedKeys, func(i, j int) bool {
		if droppedKeys[i].profile != droppedKeys[j].profile {
			return droppedKeys[i].profile < droppedKeys[j].profile
		}
		return droppedKeys[i].reason < droppedKeys[j].reason
	})
	for _, key := range droppedKeys {
		fmt.Fprintf(b, "%s{profile=%q,reason=%q} %d\n",
			forwardDroppedName, key.profile, key.reason, dropped[key])
	}
}

func sortedAttemptKeys[V any](values map[attemptKey]V) []attemptKey {
	keys := make([]attemptKey, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].profile != keys[j].profile {
			return keys[i].profile < keys[j].profile
		}
		return keys[i].result < keys[j].result
	})
	return keys
}

// defaultMetricsAddr is loopback-only by design: metrics are operational data,
// not a public endpoint.
const defaultMetricsAddr = "127.0.0.1:9091"

// startMetricsServer binds a separate metrics listener. An empty addr disables
// the listener. It returns the bound address (useful when addr ends in :0) and
// a stop function.
func startMetricsServer(addr string, metrics *Metrics) (string, func(), error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", func() {}, nil
	}
	if err := requireLoopback(addr); err != nil {
		return "", nil, err
	}
	if metrics == nil {
		metrics = forwardMetrics
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return "", nil, err
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", metrics.Handler())
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
	}
	go func() {
		_ = server.Serve(listener)
	}()

	stop := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}
	return listener.Addr().String(), stop, nil
}

// requireLoopback keeps operational metrics off public interfaces (plan
// §3.19). localhost and any literal loopback address are accepted.
func requireLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("metrics address %q: %w", addr, err)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("metrics address %q must be loopback-only", addr)
	}
	return nil
}
