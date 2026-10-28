// Package messaging is the transactional outbox, the inbox, and the RabbitMQ and Kafka plumbing around them.
package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Names of the exchanges, the Kafka topic and the routing keys. They are the contract between the modules.
const (
	Exchange          = "mint.work"
	DeadLetterExch    = "mint.dlx"
	RequeueExchange   = "mint.requeue"
	KafkaTopic        = "mint.events"
	RetryDelayMs      = 2000
	MaxAttempts       = 3
	ReserveKey        = "units.reserve"
	ReleaseKey        = "units.release"
	HeldKey           = "units.held"
	ShortKey          = "units.short"
	ReleasedKey       = "units.released"
	IssuedKey         = "certificate.issued"
	RejectedKey       = "certificate.rejected"
	DropsQueue        = "drops.commands"
	CertificatesQueue = "certificates.work"
	OrdersQueue       = "orders.events"
	PoisonToken       = "POISON"
	attemptHeader     = "x-attempt"
	outboxBatchSize   = 50
	relayIdleDelay    = 200 * time.Millisecond
	relayErrorDelay   = time.Second
	trimEvery         = 10 * time.Minute
)

// Delivery is one message handed to a Handler.
type Delivery struct {
	MessageID  uuid.UUID
	RoutingKey string
	Body       []byte
}

// Handler consumes one message. Returning ErrPoison dead-letters it at once; any other error retries it.
type Handler interface {
	Handle(ctx context.Context, d Delivery) error
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(ctx context.Context, d Delivery) error

// Handle implements Handler.
func (f HandlerFunc) Handle(ctx context.Context, d Delivery) error { return f(ctx, d) }

// ErrPoison marks a message whose rejection is already committed: it must be dead-lettered, not retried.
var ErrPoison = errors.New("poison message")

// OutOfOrderError means the message arrived before the state it depends on; it is retried later.
type OutOfOrderError struct{ Reason string }

func (e *OutOfOrderError) Error() string { return e.Reason }

// IsPoison reports whether the reference carries the poison token (a case-sensitive substring).
func IsPoison(reference string) bool { return strings.Contains(reference, PoisonToken) }

// FailureAction is what a consumer does with a message whose handler failed.
type FailureAction int

// The two things a failed message can meet.
const (
	ActionRetry FailureAction = iota
	ActionDeadLetter
)

// Decide: a poison message is dead-lettered at once; anything else is retried until it has been tried MaxAttempts times.
func Decide(priorAttempts int, err error) FailureAction {
	if errors.Is(err, ErrPoison) {
		return ActionDeadLetter
	}
	if priorAttempts+1 >= MaxAttempts {
		return ActionDeadLetter
	}
	return ActionRetry
}

// ---- the messages ----

// ReserveUnits asks the drop to hold units.
type ReserveUnits struct {
	OrderID   uuid.UUID `json:"orderId"`
	OwnerID   uuid.UUID `json:"ownerId"`
	DropID    uuid.UUID `json:"dropId"`
	Units     int       `json:"units"`
	Reference string    `json:"reference"`
}

// UnitsHeld says the units are held.
type UnitsHeld struct {
	OrderID   uuid.UUID `json:"orderId"`
	OwnerID   uuid.UUID `json:"ownerId"`
	DropID    uuid.UUID `json:"dropId"`
	Units     int       `json:"units"`
	Reference string    `json:"reference"`
}

// UnitsShort says the reserve was refused. Reason: "stock" or "cap".
type UnitsShort struct {
	OrderID        uuid.UUID `json:"orderId"`
	Reference      string    `json:"reference"`
	RequestedUnits int       `json:"requestedUnits"`
	UnitsLeft      int       `json:"unitsLeft"`
	// PerCollectorLimit is the cap of the drop.
	PerCollectorLimit int    `json:"perCollectorLimit"`
	Reason            string `json:"reason"`
}

// ReleaseUnits asks the drop to give held units back.
type ReleaseUnits struct {
	OrderID uuid.UUID `json:"orderId"`
	Reason  string    `json:"reason"`
}

// UnitsReleased says units (possibly none) were given back.
type UnitsReleased struct {
	OrderID uuid.UUID `json:"orderId"`
	Units   int       `json:"units"`
	Reason  string    `json:"reason"`
}

// CertificateIssued says the certificate exists.
type CertificateIssued struct {
	OrderID uuid.UUID `json:"orderId"`
	Number  string    `json:"number"`
}

// CertificateRejected says the certificate was refused (the poison rule).
type CertificateRejected struct {
	OrderID uuid.UUID `json:"orderId"`
	Reason  string    `json:"reason"`
}

// Parse decodes a message body.
func Parse[T any](body []byte) (T, error) {
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		return v, err
	}
	return v, nil
}
