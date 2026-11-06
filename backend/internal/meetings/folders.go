package meetings

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"phoebe/internal/apperr"
	"phoebe/internal/db"
	"phoebe/internal/ids"
	"phoebe/internal/jsonx"
)

const MaxLabelName = 60

// Label is the api.md Folder / Tag object (identical shapes).
type Label struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	MeetingCount int        `json:"meeting_count"`
	CreatedAt    jsonx.Time `json:"created_at"`
	UpdatedAt    jsonx.Time `json:"updated_at"`
}

// LabelKind selects folders or tags; the SQL fragments are constants, never user input.
type LabelKind struct {
	table string
	count string
}

var (
	Folders = LabelKind{"folders", `(SELECT count(*) FROM meetings m WHERE m.folder_id = l.id AND m.deleted_at IS NULL)`}
	Tags    = LabelKind{"tags", `(SELECT count(*) FROM meeting_tags mt JOIN meetings m ON m.id = mt.meeting_id
	                             WHERE mt.tag_id = l.id AND m.deleted_at IS NULL)`}
)

func (k LabelKind) cols() string { return `l.id, l.name, ` + k.count + `, l.created_at, l.updated_at` }

func scanLabel(row pgx.Row) (*Label, error) {
	var l Label
	var created, updated time.Time
	err := row.Scan(&l.ID, &l.Name, &l.MeetingCount, &created, &updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	l.CreatedAt = jsonx.NewTime(created)
	l.UpdatedAt = jsonx.NewTime(updated)
	return &l, nil
}

func (s *Service) getLabel(ctx context.Context, q db.Querier, k LabelKind, userID, id string) (*Label, error) {
	return scanLabel(q.QueryRow(ctx, `SELECT `+k.cols()+` FROM `+k.table+` l WHERE l.id = $1 AND l.user_id = $2`, id, userID))
}

func (s *Service) ListLabels(ctx context.Context, k LabelKind, userID string) ([]Label, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+k.cols()+` FROM `+k.table+` l WHERE l.user_id = $1 ORDER BY lower(l.name), l.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Label{}
	for rows.Next() {
		l, err := scanLabel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

func normLabelName(name *string) (string, error) {
	if name == nil {
		return "", apperr.Field("name", apperr.ReasonRequired)
	}
	n := strings.TrimSpace(*name)
	switch {
	case n == "":
		return "", apperr.Field("name", apperr.ReasonTooShort)
	case utf8.RuneCountInString(n) > MaxLabelName:
		return "", apperr.Field("name", apperr.ReasonTooLong)
	}
	return n, nil
}

type LabelInput struct {
	ID   *string `json:"id"`
	Name *string `json:"name"`
}

// claimLabelID: same owner → existing; another owner → id_conflict.
func (s *Service) claimLabelID(ctx context.Context, k LabelKind, userID, id string) (*Label, error) {
	var owner string
	err := s.pool.QueryRow(ctx, `SELECT user_id FROM `+k.table+` WHERE id = $1`, id).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if owner != userID {
		return nil, apperr.New(apperr.IDConflict)
	}
	return s.getLabel(ctx, s.pool, k, userID, id)
}

// CreateLabel implements POST /v1/folders|tags; created=false on a same-owner id replay.
func (s *Service) CreateLabel(ctx context.Context, k LabelKind, userID string, in LabelInput) (*Label, bool, error) {
	id := ids.New()
	if in.ID != nil {
		v, ok := ids.Parse(*in.ID)
		if !ok {
			return nil, false, apperr.Field("id", apperr.ReasonInvalid)
		}
		id = v
		if l, err := s.claimLabelID(ctx, k, userID, id); l != nil || err != nil {
			return l, false, err
		}
	}
	name, err := normLabelName(in.Name)
	if err != nil {
		return nil, false, err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO `+k.table+` (id, user_id, name) VALUES ($1, $2, $3)`, id, userID, name)
	switch {
	case db.IsUniqueViolation(err, k.table+"_pkey"):
		l, err := s.claimLabelID(ctx, k, userID, id)
		if l == nil && err == nil {
			err = apperr.New(apperr.IDConflict)
		}
		return l, false, err
	case db.IsUniqueViolation(err, k.table+"_user_name_key"):
		return nil, false, apperr.New(apperr.NameTaken)
	case err != nil:
		return nil, false, err
	}
	l, err := s.getLabel(ctx, s.pool, k, userID, id)
	return l, true, err
}

func (s *Service) RenameLabel(ctx context.Context, k LabelKind, userID, id string, name *string) (*Label, error) {
	n, err := normLabelName(name)
	if err != nil {
		return nil, err
	}
	_, err = s.pool.Exec(ctx, `UPDATE `+k.table+` SET name = $3, updated_at = now() WHERE id = $1 AND user_id = $2 AND name <> $3`,
		id, userID, n)
	if db.IsUniqueViolation(err, k.table+"_user_name_key") {
		return nil, apperr.New(apperr.NameTaken)
	}
	if err != nil {
		return nil, err
	}
	return s.getLabel(ctx, s.pool, k, userID, id)
}

// DeleteLabel removes a folder (its meetings become unfiled) or a tag (removed from all
// meetings); affected meetings get an updated_at bump (backend.md §3.5).
func (s *Service) DeleteLabel(ctx context.Context, k LabelKind, userID, id string) error {
	return db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		var affected string
		if k == Folders {
			affected = `SELECT m.id FROM meetings m WHERE m.folder_id = $1 AND m.user_id = $2`
		} else {
			affected = `SELECT m.id FROM meetings m JOIN meeting_tags mt ON mt.meeting_id = m.id WHERE mt.tag_id = $1 AND m.user_id = $2`
		}
		rows, err := tx.Query(ctx, affected+` ORDER BY m.id FOR UPDATE OF m`, id, userID)
		if err != nil {
			return err
		}
		mids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		if len(mids) > 0 {
			set := `updated_at = now()`
			if k == Folders {
				set = `folder_id = NULL, updated_at = now()`
			}
			if _, err := tx.Exec(ctx, `UPDATE meetings SET `+set+` WHERE id = ANY($1::uuid[])`, mids); err != nil {
				return err
			}
		}
		tag, err := tx.Exec(ctx, `DELETE FROM `+k.table+` WHERE id = $1 AND user_id = $2`, id, userID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return apperr.ErrNotFound
		}
		return nil
	})
}
