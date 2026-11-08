package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/charlesrobsampson/msg/client"
)

// replaySummary reports one outbox replay pass. Pending is the number of rows
// found before enqueueing; Replayed is how many the bounded queue accepted.
// Rows rejected by a full queue stay pending for the next pass.
type replaySummary struct {
	Pending  int
	Replayed int
}

// forwardOutboxEntry snapshots one forward destination and payload for durable
// replay. The token is read from the environment at send time; the URL is
// frozen at save time so a later config change affects only new messages.
func forwardOutboxEntry(profile, url string, msg ForwardMessage, now time.Time) (*client.ForwardOutboxEntry, error) {
	payload, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal forward for outbox: %w", err)
	}
	return &client.ForwardOutboxEntry{
		Profile:     metricProfile(profile),
		MessageID:   msg.MessageID,
		URL:         url,
		Payload:     string(payload),
		CreatedAtMS: now.UnixMilli(),
	}, nil
}

// replayOutbox re-enqueues pending rows oldest first. Enqueueing never blocks;
// a saturated queue leaves the remaining rows pending for the next replay.
func replayOutbox(ctx context.Context, queue *ForwardQueue, db *client.SignalDB, profile, token string) (replaySummary, error) {
	entries, err := db.PendingForwards()
	if err != nil {
		return replaySummary{}, err
	}
	summary := replaySummary{Pending: len(entries)}
	queue.metrics.SetOutboxPending(profile, len(entries))

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return summary, err
		}

		var msg ForwardMessage
		if err := json.Unmarshal([]byte(entry.Payload), &msg); err != nil {
			entryProfile := entry.Profile
			if entryProfile == "" {
				entryProfile = profile
			}
			fmt.Printf("[outbox] dropping corrupt entry for profile %s message %s: %v\n",
				metricProfile(entryProfile), entry.MessageID, err)
			if delErr := db.DeleteForward(entry.MessageID); delErr != nil {
				fmt.Printf("[outbox] delete corrupt entry for message %s: %v\n", entry.MessageID, delErr)
			}
			continue
		}
		if msg.MessageID == "" {
			msg.MessageID = entry.MessageID
		}

		entryProfile := entry.Profile
		if entryProfile == "" {
			entryProfile = profile
		}
		if queue.EnqueueDurable(db, entryProfile, entry.URL, token, msg, entry.MessageID) {
			summary.Replayed++
			queue.metrics.IncOutboxReplayed(entryProfile)
		}
	}

	if pending, err := db.PendingForwardCount(); err == nil {
		queue.metrics.SetOutboxPending(profile, pending)
	}
	return summary, nil
}

// handleReplay drains the active profile's durable forward outbox. It is the
// manual counterpart to the automatic replay that runs when an account DB opens.
func handleReplay(args []string) {
	if hasHelpFlag(args) {
		printCommandHelp("replay")
		return
	}

	db, err := client.OpenSignalDB()
	if err != nil {
		fmt.Printf("Cannot open the message database: %v\n", err)
		return
	}
	defer db.Close()

	token := os.Getenv("AETHER_INGRESS_TOKEN")
	profile := client.GetProfile()
	queue := NewForwardQueue(defaultForwardQueueDepth)
	summary, err := replayOutbox(context.Background(), queue, db, profile, token)
	queue.Close()
	if err != nil {
		fmt.Printf("Replay failed: %v\n", err)
		return
	}
	fmt.Printf("Replayed %d of %d pending forward(s) for profile %q.\n",
		summary.Replayed, summary.Pending, metricProfile(profile))
}
