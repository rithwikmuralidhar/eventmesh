// Package deliveries owns the delivery lifecycle: its state machine, the
// read side of the API and manual replay.
package deliveries

import "fmt"

// Status is the state of a delivery. The same values are enforced by a CHECK
// constraint on deliveries.status.
type Status string

// Delivery states.
const (
	StatusPending        Status = "pending"
	StatusProcessing     Status = "processing"
	StatusRetryScheduled Status = "retry_scheduled"
	StatusDelivered      Status = "delivered"
	StatusDeadLetter     Status = "dead_letter"
	StatusCancelled      Status = "cancelled"
)

// AllStatuses lists every state, in lifecycle order.
var AllStatuses = []Status{
	StatusPending, StatusProcessing, StatusRetryScheduled,
	StatusDelivered, StatusDeadLetter, StatusCancelled,
}

// allowedTransitions is the complete state machine. Anything not listed here
// is a bug. Every UPDATE that changes status also names the expected current
// status in its WHERE clause, so the database enforces the same table under
// concurrency; this map is the single readable definition of it.
var allowedTransitions = map[Status][]Status{
	// A worker claims the row, or the endpoint is deleted first.
	StatusPending:        {StatusProcessing, StatusCancelled},
	StatusRetryScheduled: {StatusProcessing, StatusCancelled},
	StatusProcessing: {
		StatusDelivered,      // 2xx
		StatusRetryScheduled, // retryable failure, attempts remain
		StatusDeadLetter,     // non-retryable failure or attempts exhausted
		StatusPending,        // rate-limited and deferred, or lease expired
		StatusCancelled,      // endpoint found disabled or deleted
	},
	// Manual replay is the only way out of dead letter.
	StatusDeadLetter: {StatusPending},
	StatusDelivered:  {},
	StatusCancelled:  {},
}

// CanTransition reports whether from -> to is an allowed transition.
func CanTransition(from, to Status) bool {
	for _, allowed := range allowedTransitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// ValidateTransition returns an error for a transition the state machine does
// not allow. Code that is about to change a status calls it first, so an
// illegal transition fails loudly instead of corrupting the row.
func ValidateTransition(from, to Status) error {
	if !CanTransition(from, to) {
		return fmt.Errorf("illegal delivery transition %q -> %q", from, to)
	}
	return nil
}

// IsTerminal reports whether no worker will touch the delivery again without
// a manual action. dead_letter counts: it only leaves by replay.
func (s Status) IsTerminal() bool {
	return s == StatusDelivered || s == StatusDeadLetter || s == StatusCancelled
}

// Valid reports whether s is one of the defined states.
func (s Status) Valid() bool {
	for _, known := range AllStatuses {
		if s == known {
			return true
		}
	}
	return false
}
