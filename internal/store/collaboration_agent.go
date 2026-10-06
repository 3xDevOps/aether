package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

func (d *DB) MarkRoomMessageAgentQueued(ctx context.Context, id string) (bool, error) {
	if id == "" {
		return false, errors.New("store: mark room message queued: id is required")
	}
	now, err := encodeTime(time.Now().UTC())
	if err != nil {
		return false, err
	}
	res, err := d.db.ExecContext(ctx, `UPDATE room_messages SET agent_delivery = ?, updated_at = ?
		WHERE id = ? AND state = ? AND agent_delivery = ''`, AgentQueued, now, id, RoomMessageSent)
	if err != nil {
		return false, fmt.Errorf("store: mark room message %s queued: %w", id, err)
	}
	return affected(res)
}

// A session can settle the steer before the room records the send, while the
// row is still the claim's uncertain state; both orders end in the same row.
func (d *DB) SettleRoomMessageAgentDelivery(ctx context.Context, id string, failure *RoomMessageFailure) (bool, error) {
	if id == "" {
		return false, errors.New("store: settle room message delivery: id is required")
	}
	now, err := encodeTime(time.Now().UTC())
	if err != nil {
		return false, err
	}
	var res sql.Result
	if failure == nil {
		res, err = d.db.ExecContext(ctx, `UPDATE room_messages SET agent_delivery = ?, updated_at = ?
			WHERE id = ? AND state IN (?, ?) AND agent_delivery IN ('', ?)`,
			AgentDelivered, now, id, RoomMessageSent, RoomMessageUncertain, AgentQueued)
	} else {
		failureJSON, marshalErr := marshalCollaborationJSON(failure, "")
		if marshalErr != nil {
			return false, fmt.Errorf("store: settle room message delivery failure: %w", marshalErr)
		}
		res, err = d.db.ExecContext(ctx, `UPDATE room_messages SET state = ?, failure = ?, updated_at = ?
			WHERE id = ? AND state IN (?, ?) AND agent_delivery IN ('', ?)`,
			RoomMessageNotSent, nullableJSON(failureJSON), now, id, RoomMessageSent, RoomMessageUncertain, AgentQueued)
	}
	if err != nil {
		return false, fmt.Errorf("store: settle room message %s delivery: %w", id, err)
	}
	return affected(res)
}

// Agent sessions end with the server process, so a steer still queued at
// startup can never reach its agent.
func (d *DB) DropAgentQueuedRoomMessages(ctx context.Context, failure *RoomMessageFailure) ([]string, error) {
	now, err := encodeTime(time.Now().UTC())
	if err != nil {
		return nil, err
	}
	failureJSON, err := marshalCollaborationJSON(failure, "")
	if err != nil {
		return nil, fmt.Errorf("store: drop queued room messages: %w", err)
	}
	rows, err := d.db.QueryContext(ctx, `UPDATE room_messages SET state = ?, failure = ?, updated_at = ?
		WHERE state = ? AND agent_delivery = ? RETURNING id`,
		RoomMessageNotSent, nullableJSON(failureJSON), now, RoomMessageSent, AgentQueued)
	if err != nil {
		return nil, fmt.Errorf("store: drop queued room messages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: drop queued room messages: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: drop queued room messages: %w", err)
	}
	return ids, nil
}

func affected(res sql.Result) (bool, error) {
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: rows affected: %w", err)
	}
	return n > 0, nil
}
