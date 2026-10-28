package drops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joshbarros/golang-mint/internal/platform/messaging"
	"github.com/joshbarros/golang-mint/internal/platform/pgdb"
)

// Schema and consumer names.
const (
	Schema       = "drops"
	ConsumerName = "drops"

	HoldHeld     = "Held"
	HoldReleased = "Released"
)

// ReserveOutcome is what a reserve did.
type ReserveOutcome int

// The ways a reserve can end.
const (
	Held ReserveOutcome = iota
	Short
	CapReached
	Duplicate
)

// Service is the drops module's logic. It is the only writer of units_left and of the per-collector counts.
type Service struct {
	Pool   *pgxpool.Pool
	Outbox messaging.Outbox
	Log    *slog.Logger
}

// NewService builds a Service with its outbox.
func NewService(pool *pgxpool.Pool, log *slog.Logger) *Service {
	return &Service{Pool: pool, Outbox: messaging.Outbox{Schema: Schema, Module: "drops"}, Log: log}
}

// Handle implements messaging.Handler for the drops queue.
func (s *Service) Handle(ctx context.Context, d messaging.Delivery) error {
	switch d.RoutingKey {
	case messaging.ReserveKey:
		cmd, err := messaging.Parse[messaging.ReserveUnits](d.Body)
		if err != nil {
			return fmt.Errorf("%w: %v", messaging.ErrPoison, err)
		}
		_, err = s.Reserve(ctx, d.MessageID, cmd)
		return err
	case messaging.ReleaseKey:
		cmd, err := messaging.Parse[messaging.ReleaseUnits](d.Body)
		if err != nil {
			return fmt.Errorf("%w: %v", messaging.ErrPoison, err)
		}
		_, err = s.Release(ctx, d.MessageID, cmd)
		return err
	}
	return fmt.Errorf("drops does not handle %q", d.RoutingKey)
}

var errRefused = errors.New("the cap or the stock said no")

// Reserve takes units from a drop in two atomic steps inside one transaction: a per-collector cap upsert that only moves
// while the new total still fits, then one conditional UPDATE that matches only while the remaining stock covers the
// request. If either says no, the whole attempt rolls back (so a cap increment never outlives a failed reserve) and the
// refusal is recorded in a fresh transaction.
func (s *Service) Reserve(ctx context.Context, messageID uuid.UUID, cmd messaging.ReserveUnits) (ReserveOutcome, error) {
	outcome := Held
	err := pgdb.Tx(ctx, s.Pool, func(tx pgx.Tx) error {
		first, err := s.Outbox.Claim(ctx, tx, ConsumerName, messageID)
		if err != nil {
			return err
		}
		if !first {
			outcome = Duplicate
			return nil
		}
		// 1. the per-collector cap: one atomic upsert that only moves while the new total still fits
		capTag, err := tx.Exec(ctx, `
			INSERT INTO drops.collectors (drop_id, owner_id, units)
			SELECT d.id, $2, $3 FROM drops.drops d WHERE d.id = $1 AND d.max_per_collector >= $3
			ON CONFLICT (drop_id, owner_id) DO UPDATE SET units = drops.collectors.units + $3
			WHERE drops.collectors.units + $3 <= (SELECT max_per_collector FROM drops.drops WHERE id = $1)`,
			cmd.DropID, cmd.OwnerID, cmd.Units)
		if err != nil {
			return err
		}
		if capTag.RowsAffected() == 1 {
			// 2. the remaining units: one conditional UPDATE, the only writer of units_left
			stockTag, err := tx.Exec(ctx, `UPDATE drops.drops SET units_left = units_left - $2 WHERE id = $1 AND units_left >= $2`, cmd.DropID, cmd.Units)
			if err != nil {
				return err
			}
			if stockTag.RowsAffected() == 1 {
				if _, err := tx.Exec(ctx, `INSERT INTO drops.unit_holds (order_id, owner_id, drop_id, units, status, created_at) VALUES ($1,$2,$3,$4,'Held',now())`,
					cmd.OrderID, cmd.OwnerID, cmd.DropID, cmd.Units); err != nil {
					return err
				}
				return s.Outbox.Enqueue(ctx, tx, messaging.HeldKey, cmd.OrderID,
					messaging.UnitsHeld{OrderID: cmd.OrderID, OwnerID: cmd.OwnerID, DropID: cmd.DropID, Units: cmd.Units, Reference: cmd.Reference})
			}
		}
		return errRefused // roll the cap increment back with everything else
	})
	if err == nil {
		return outcome, nil
	}
	if !errors.Is(err, errRefused) {
		return 0, err
	}
	// The refusal is recorded in a fresh transaction (the inbox claim was rolled back with the attempt).
	err = pgdb.Tx(ctx, s.Pool, func(tx pgx.Tx) error {
		first, err := s.Outbox.Claim(ctx, tx, ConsumerName, messageID)
		if err != nil {
			return err
		}
		if !first {
			outcome = Duplicate
			return nil
		}
		var left, limit int
		_ = tx.QueryRow(ctx, `SELECT units_left, max_per_collector FROM drops.drops WHERE id = $1`, cmd.DropID).Scan(&left, &limit)
		var holding int
		_ = tx.QueryRow(ctx, `SELECT units FROM drops.collectors WHERE drop_id = $1 AND owner_id = $2`, cmd.DropID, cmd.OwnerID).Scan(&holding)
		reason := "stock"
		outcome = Short
		if limit > 0 && holding+cmd.Units > limit {
			reason = "cap"
			outcome = CapReached
		}
		return s.Outbox.Enqueue(ctx, tx, messaging.ShortKey, cmd.OrderID,
			messaging.UnitsShort{OrderID: cmd.OrderID, Reference: cmd.Reference, RequestedUnits: cmd.Units, UnitsLeft: left, Reason: reason, PerCollectorLimit: limit})
	})
	return outcome, err
}

// Release gives held units back exactly once, returning the collector's allowance too. A second release changes nothing.
func (s *Service) Release(ctx context.Context, messageID uuid.UUID, cmd messaging.ReleaseUnits) (bool, error) {
	released := false
	err := pgdb.Tx(ctx, s.Pool, func(tx pgx.Tx) error {
		first, err := s.Outbox.Claim(ctx, tx, ConsumerName, messageID)
		if err != nil || !first {
			return err
		}
		var dropID, ownerID uuid.UUID
		var units int
		err = tx.QueryRow(ctx, `
			UPDATE drops.unit_holds SET status = 'Released', released_at = now(), release_reason = $2
			WHERE order_id = $1 AND status = 'Held' RETURNING drop_id, owner_id, units`, cmd.OrderID, cmd.Reason).Scan(&dropID, &ownerID, &units)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // nothing held, or already released
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE drops.drops SET units_left = units_left + $2 WHERE id = $1 AND units_left + $2 <= units_total`, dropID, units); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE drops.collectors SET units = units - $3 WHERE drop_id = $1 AND owner_id = $2 AND units >= $3`, dropID, ownerID, units); err != nil {
			return err
		}
		released = true
		return s.Outbox.Enqueue(ctx, tx, messaging.ReleasedKey, cmd.OrderID, messaging.UnitsReleased{OrderID: cmd.OrderID, Units: units, Reason: cmd.Reason})
	})
	return released, err
}

var _ = time.Second
