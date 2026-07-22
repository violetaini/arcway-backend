package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// prepareUserForwardDeletionTx makes forwarding deletion durable before any
// remote cleanup is attempted. Port reservations deliberately remain owned by
// their hops until the Guard confirms removal.
func prepareUserForwardDeletionTx(ctx context.Context, tx *sql.Tx, username, actor string, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `UPDATE user_tunnel_grants
SET enabled = 0,
    version = version + 1,
    updated_at = ?
WHERE username = ? AND enabled != 0`, now, username); err != nil {
		return fmt.Errorf("disable user tunnel grants: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO tunnel_audit_events (
    actor, action, entity_type, entity_id, username, details
)
SELECT ?, 'user_deletion', 'user_forward', id, username, 'remote cleanup queued'
FROM user_forward_rules
WHERE username = ? AND desired_state != ?`, actor, username, ForwardDesiredDeleted); err != nil {
		return fmt.Errorf("audit user forwarding deletion: %w", err)
	}

	// Bump each hop exactly once when it enters cleanup. Repeating preparation
	// must not keep advancing generations while an offline Guard is retrying.
	if _, err := tx.ExecContext(ctx, `UPDATE user_forward_hops
SET desired_state = ?,
    generation = generation + 1,
    retry_count = 0,
    last_error = '',
    updated_at = ?
WHERE forward_id IN (
    SELECT id FROM user_forward_rules WHERE username = ?
) AND desired_state != ?`, ForwardDesiredDeleted, now, username, ForwardDesiredDeleted); err != nil {
		return fmt.Errorf("queue user forward hop cleanup: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `UPDATE user_forward_rules
SET desired_state = ?,
    observed_state = ?,
    suspend_reason = 'user_deleted',
    generation = generation + 1,
    last_error_code = '',
    last_error_detail = '',
    updated_at = ?
WHERE username = ? AND desired_state != ?`, ForwardDesiredDeleted,
		ForwardObservedCleanupPending, now, username, ForwardDesiredDeleted); err != nil {
		return fmt.Errorf("queue user forward cleanup: %w", err)
	}

	// Repair an interrupted/inconsistent preparation without changing the
	// generation already assigned to the remote delete operation.
	if _, err := tx.ExecContext(ctx, `UPDATE user_forward_rules
SET observed_state = ?,
    suspend_reason = 'user_deleted',
    updated_at = ?
WHERE username = ?
  AND desired_state = ?
  AND observed_state != ?
  AND EXISTS (
      SELECT 1 FROM user_forward_hops h WHERE h.forward_id = user_forward_rules.id
  )`, ForwardObservedCleanupPending, now, username, ForwardDesiredDeleted,
		ForwardObservedCleanupPending); err != nil {
		return fmt.Errorf("repair user forward cleanup state: %w", err)
	}

	return nil
}

// userForwardDeletionReadyTx accepts the small crash window where every Guard
// removal was acknowledged but the normal forward finalizer has not yet
// deleted the local rows. Anything else must keep the user tombstone pending.
func userForwardDeletionReadyTx(ctx context.Context, tx *sql.Tx, username string) (bool, error) {
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*)
FROM user_forward_rules f
WHERE f.username = ? AND (
    f.desired_state != ?
    OR EXISTS (
        SELECT 1
        FROM user_forward_hops h
        WHERE h.forward_id = f.id AND (
            h.desired_state != ?
            OR h.observed_state != ?
            OR h.generation != h.applied_generation
        )
    )
)`, username, ForwardDesiredDeleted, ForwardDesiredDeleted,
		ForwardObservedSuspended).Scan(&pending); err != nil {
		return false, fmt.Errorf("check user forward cleanup: %w", err)
	}
	return pending == 0, nil
}

// finalizeUserForwardDeletionTx is intentionally defensive. Normal forwarding
// reconciliation already removes hops and releases ports, but finalization
// also purges a fully acknowledged crash residue so a recreated username can
// never inherit forwarding state.
func finalizeUserForwardDeletionTx(ctx context.Context, tx *sql.Tx, username string) error {
	steps := []struct {
		name  string
		query string
	}{
		{
			name: "port allocations",
			query: `DELETE FROM server_port_allocations
WHERE owner_type = 'forward_hop' AND owner_id IN (
    SELECT h.id
    FROM user_forward_hops h
    JOIN user_forward_rules f ON f.id = h.forward_id
    WHERE f.username = ?
)`,
		},
		{
			name: "usage",
			query: `DELETE FROM user_forward_usage
WHERE forward_id IN (SELECT id FROM user_forward_rules WHERE username = ?)`,
		},
		{
			name: "hops",
			query: `DELETE FROM user_forward_hops
WHERE forward_id IN (SELECT id FROM user_forward_rules WHERE username = ?)`,
		},
		{
			name:  "rules",
			query: `DELETE FROM user_forward_rules WHERE username = ?`,
		},
		{
			name: "archived usage",
			query: `DELETE FROM user_tunnel_grant_usage_archive
WHERE grant_id IN (SELECT id FROM user_tunnel_grants WHERE username = ?)`,
		},
		{
			name:  "grants",
			query: `DELETE FROM user_tunnel_grants WHERE username = ?`,
		},
	}
	for _, step := range steps {
		if _, err := tx.ExecContext(ctx, step.query, username); err != nil {
			return fmt.Errorf("delete user forwarding %s: %w", step.name, err)
		}
	}
	return nil
}
