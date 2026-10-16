// Package events is the LISTEN/NOTIFY event bus. Producers call Notify inside
// their transaction, so the notification is only delivered on commit. Every
// replica runs a Bus that LISTENs on one channel per project that has local
// subscribers and fans events out to them.
package events

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// Event types.
const (
	// TypeMessageCreated announces a newly captured message.
	TypeMessageCreated = "message.created"
	// TypeResync tells subscribers they may have missed events and should reload.
	TypeResync = "resync"
	// TypeReady is sent once a subscription is live (SSE only, never via NOTIFY).
	TypeReady = "ready"
)

const channelPrefix = "sp_msg_"

// Event is the payload carried over NOTIFY and SSE.
type Event struct {
	Type      string `json:"type"`
	MessageID int64  `json:"message_id,omitempty"`
}

// Channel returns the Postgres channel name for a project.
func Channel(projectID int64) string {
	return channelPrefix + strconv.FormatInt(projectID, 10)
}

// projectFromChannel is the inverse of Channel.
func projectFromChannel(ch string) (int64, bool) {
	rest, ok := strings.CutPrefix(ch, channelPrefix)
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	return id, err == nil
}

// Execer is satisfied by pgx pools, connections and transactions.
type Execer interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// Notify publishes ev on the project channel. Call it with a transaction to
// have the event delivered only if the transaction commits.
func Notify(ctx context.Context, db Execer, projectID int64, ev Event) error {
	payload, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("encode event: %w", err)
	}
	if _, err := db.Exec(ctx, "SELECT pg_notify($1, $2)", Channel(projectID), string(payload)); err != nil {
		return fmt.Errorf("notify: %w", err)
	}
	return nil
}
