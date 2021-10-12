// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package sqlstore

import (
	"database/sql"
	"fmt"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/mattermost/focalboard/server/model"
	"github.com/mattermost/focalboard/server/services/store"
	"github.com/mattermost/focalboard/server/utils"

	"github.com/mattermost/mattermost-server/v6/shared/mlog"
)

func notificationHintFields() []string {
	return []string{
		"block_type",
		"block_id",
		"workspace_id",
		"create_at",
		"notify_at",
	}
}

func valuesForNotificationHint(hint *model.NotificationHint) []interface{} {
	return []interface{}{
		hint.BlockType,
		hint.BlockID,
		hint.WorkspaceID,
		hint.CreateAt,
		hint.NotifyAt,
	}
}

func (s *SQLStore) notificationHintFromRows(rows *sql.Rows) ([]*model.NotificationHint, error) {
	hints := []*model.NotificationHint{}

	for rows.Next() {
		var hint model.NotificationHint
		err := rows.Scan(
			&hint.BlockType,
			&hint.BlockID,
			&hint.WorkspaceID,
			&hint.CreateAt,
			&hint.NotifyAt,
		)
		if err != nil {
			return nil, err
		}
		hints = append(hints, &hint)
	}
	return hints, nil
}

// UpsertNotificationHint creates or updates a notification hint. When updating the `notify_at` is set
// to the current time plus `notificationFreq`.
func (s *SQLStore) UpsertNotificationHint(hint *model.NotificationHint, notificationFreq time.Duration) (*model.NotificationHint, error) {
	if err := hint.IsValid(); err != nil {
		return nil, err
	}

	c := store.Container{
		WorkspaceID: hint.WorkspaceID,
	}

	hintRet, err := s.GetNotificationHint(c, hint.BlockID)
	if err != nil && !s.IsErrNotFound(err) {
		return nil, err
	}

	now := model.GetMillis()
	notifyAt := utils.GetMillisForTime(time.Now().Add(notificationFreq))

	if hintRet == nil {
		// insert
		hintRet = hint.Copy()
		hintRet.CreateAt = now
		hintRet.NotifyAt = notifyAt

		query := s.getQueryBuilder().Insert(s.tablePrefix + "notification_hints").
			Columns(notificationHintFields()...).
			Values(valuesForNotificationHint(hintRet)...)
		_, err = query.Exec()
	} else {
		// update
		hintRet.NotifyAt = notifyAt

		query := s.getQueryBuilder().Update(s.tablePrefix+"notification_hints").
			Set("notify_at", now).
			Where(sq.Eq{"block_id": hintRet.BlockID}).
			Where(sq.Eq{"workspace_id": hintRet.WorkspaceID})
		_, err = query.Exec()
	}

	if err != nil {
		s.logger.Error("Cannot upsert notification hint",
			mlog.String("block_id", hint.BlockID),
			mlog.String("workspace_id", hint.WorkspaceID),
			mlog.Err(err),
		)
		return nil, err
	}
	return hintRet, nil
}

// DeleteNotificationHint deletes the notification hint for the specified block.
func (s *SQLStore) DeleteNotificationHint(c store.Container, blockID string) error {
	query := s.getQueryBuilder().
		Delete(s.tablePrefix + "notification_hints").
		Where(sq.Eq{"block_id": blockID}).
		Where(sq.Eq{"workspace_id": c.WorkspaceID})

	result, err := query.Exec()
	if err != nil {
		return err
	}

	count, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if count == 0 {
		return store.NewErrNotFound(blockID)
	}

	return nil
}

// GetNotificationHint fetches the notification hint for the specified block.
func (s *SQLStore) GetNotificationHint(c store.Container, blockID string) (*model.NotificationHint, error) {
	query := s.getQueryBuilder().
		Select(notificationHintFields()...).
		From(s.tablePrefix + "notification_hints").
		Where(sq.Eq{"block_id": blockID}).
		Where(sq.Eq{"workspace_id": c.WorkspaceID})

	rows, err := query.Query()
	if err != nil {
		s.logger.Error("Cannot fetch notification hint",
			mlog.String("block_id", blockID),
			mlog.String("workspace_id", c.WorkspaceID),
			mlog.Err(err),
		)
		return nil, err
	}
	defer s.CloseRows(rows)

	hint, err := s.notificationHintFromRows(rows)
	if err != nil {
		s.logger.Error("Cannot get notification hint",
			mlog.String("block_id", blockID),
			mlog.String("workspace_id", c.WorkspaceID),
			mlog.Err(err),
		)
		return nil, err
	}
	if len(hint) == 0 {
		return nil, store.NewErrNotFound(blockID)
	}
	return hint[0], nil
}

// GetNextNotificationHint fetches the next scheduled notification hint. If remove is true
// then the hint is removed from the database as well, as if popping from a stack.
func (s *SQLStore) GetNextNotificationHint(remove bool) (*model.NotificationHint, error) {
	selectQuery := s.getQueryBuilder().
		Select(notificationHintFields()...).
		From(s.tablePrefix + "notification_hints").
		OrderBy("notify_at").
		Limit(1)

	rows, err := selectQuery.Query()
	if err != nil {
		s.logger.Error("Cannot fetch next notification hint",
			mlog.Err(err),
		)
		return nil, err
	}
	defer s.CloseRows(rows)

	hints, err := s.notificationHintFromRows(rows)
	if err != nil {
		s.logger.Error("Cannot get next notification hint",
			mlog.Err(err),
		)
		return nil, err
	}
	if len(hints) == 0 {
		return nil, store.NewErrNotFound("")
	}

	hint := hints[0]

	if remove {
		deleteQuery := s.getQueryBuilder().
			Delete(s.tablePrefix + "notification_hints").
			Where(sq.Eq{"block_id": hint.BlockID})

		result, err := deleteQuery.Exec()
		if err != nil {
			return nil, fmt.Errorf("cannot delete while getting next notification hint: %w", err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return nil, fmt.Errorf("cannot verify delete while getting next notification hint: %w", err)
		}
		if rows == 0 {
			// another node likely has grabbed this hint for processing concurrently; let that node handle it
			// and we'll return an error here so we try again.
			return nil, fmt.Errorf("cannot delete missing hint while getting next notification hint: %w", err)
		}
	}

	return hint, nil
}
