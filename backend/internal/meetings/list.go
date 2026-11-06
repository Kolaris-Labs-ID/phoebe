package meetings

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"phoebe/internal/apperr"
	"phoebe/internal/ids"
	"phoebe/internal/jsonx"
)

// Cursor is the keyset position (sort_at desc, id desc) of the last returned item.
type Cursor struct {
	SortAt time.Time `json:"s"`
	ID     string    `json:"i"`
}

type ListQuery struct {
	Q        string // "" = no search
	FolderID string // "" = any, "none" = unfiled, else UUID
	TagID    string // "" = any
	Limit    int
	After    *Cursor
}

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

var jobTypeOrder = []string{"transcribe", "generate_notes", "generate_mom"}

// List implements GET /v1/meetings. Unowned folder/tag filters are 422 not_found, like
// any other body/query id (backend.md §2).
func (s *Service) List(ctx context.Context, userID string, lq ListQuery) ([]ListItem, *Cursor, error) {
	var fe fieldErrs
	if utf8.RuneCountInString(lq.Q) > MaxQuery {
		fe.add("q", apperr.ReasonTooLong)
	}
	var folderID, tagID *string
	if lq.FolderID != "" && lq.FolderID != "none" {
		folderID = normFolderID(&fe, lq.FolderID)
	}
	if lq.TagID != "" {
		if id, ok := ids.Parse(lq.TagID); ok {
			tagID = &id
		} else {
			fe.add("tag_id", apperr.ReasonInvalid)
		}
	}
	if lq.After != nil && !ids.Valid(lq.After.ID) {
		fe.add("cursor", apperr.ReasonInvalid)
	}
	if err := fe.err(); err != nil {
		return nil, nil, err
	}
	if folderID != nil || tagID != nil {
		var folderOK, tagOK bool
		if err := s.pool.QueryRow(ctx, `
			SELECT $2::uuid IS NULL OR EXISTS (SELECT 1 FROM folders WHERE id = $2 AND user_id = $1),
			       $3::uuid IS NULL OR EXISTS (SELECT 1 FROM tags WHERE id = $3 AND user_id = $1)`,
			userID, folderID, tagID).Scan(&folderOK, &tagOK); err != nil {
			return nil, nil, err
		}
		if !folderOK {
			fe.add("folder_id", apperr.ReasonNotFound)
		}
		if !tagOK {
			fe.add("tag_id", apperr.ReasonNotFound)
		}
		if err := fe.err(); err != nil {
			return nil, nil, err
		}
	}

	args := []any{userID}
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	where := []string{"m.user_id = $1", "m.deleted_at IS NULL"}
	if lq.Q != "" {
		p := arg("%" + likeEscaper.Replace(lq.Q) + "%")
		where = append(where, `(m.title ILIKE `+p+`
			OR EXISTS (SELECT 1 FROM meeting_tags mt JOIN tags t ON t.id = mt.tag_id WHERE mt.meeting_id = m.id AND t.name ILIKE `+p+`)
			OR EXISTS (SELECT 1 FROM search_documents sd WHERE sd.meeting_id = m.id AND sd.body ILIKE `+p+`))`)
	}
	switch {
	case lq.FolderID == "none":
		where = append(where, "m.folder_id IS NULL")
	case folderID != nil:
		where = append(where, "m.folder_id = "+arg(*folderID))
	}
	if tagID != nil {
		where = append(where, "EXISTS (SELECT 1 FROM meeting_tags mt WHERE mt.meeting_id = m.id AND mt.tag_id = "+arg(*tagID)+")")
	}
	if lq.After != nil {
		where = append(where, "(m.sort_at, m.id) < ("+arg(lq.After.SortAt)+"::timestamptz, "+arg(lq.After.ID)+"::uuid)")
	}
	limit := arg(lq.Limit + 1)

	rows, err := s.pool.Query(ctx, `
		SELECT m.id, m.source, m.title, m.started_at, m.timezone, m.folder_id,
		       COALESCE((SELECT array_agg(mt.tag_id::text ORDER BY mt.tag_id) FROM meeting_tags mt WHERE mt.meeting_id = m.id), '{}'),
		       m.pipeline_state, a.duration_ms, (a.id IS NOT NULL AND a.deleted_at IS NULL),
		       (SELECT CASE WHEN r.reviewed_at IS NULL THEN 'draft' ELSE 'reviewed' END
		          FROM documents d JOIN document_revisions r ON r.document_id = d.id AND r.revision_number = d.current_revision_number
		         WHERE d.meeting_id = m.id AND d.kind = 'mom'),
		       COALESCE((SELECT array_agg(DISTINCT j.type) FROM jobs j
		                  WHERE j.meeting_id = m.id AND j.status IN ('queued', 'running')), '{}'),
		       (SELECT j.status FROM jobs j WHERE j.meeting_id = m.id AND j.type = 'generate_mom'
		         ORDER BY j.created_at DESC, j.id DESC LIMIT 1),
		       (SELECT CASE WHEN j.status = 'failed' THEN j.last_error_category END FROM jobs j
		         WHERE j.meeting_id = m.id AND j.type = 'transcribe' ORDER BY j.created_at DESC, j.id DESC LIMIT 1),
		       m.created_at, m.updated_at, m.sort_at
		FROM meetings m LEFT JOIN audio_assets a ON a.meeting_id = m.id
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY m.sort_at DESC, m.id DESC
		LIMIT `+limit, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	items := []ListItem{}
	var sortAts []time.Time
	for rows.Next() {
		var it ListItem
		var started *time.Time
		var created, updated, sortAt time.Time
		var momStatus *string
		if err := rows.Scan(&it.ID, &it.Source, &it.Title, &started, &it.Timezone, &it.FolderID, &it.TagIDs,
			&it.PipelineState, &it.DurationMS, &it.AudioAvailable, &it.MoMReviewStatus, &it.ActiveJobTypes,
			&momStatus, &it.TranscriptionErrorCategory, &created, &updated, &sortAt); err != nil {
			return nil, nil, err
		}
		it.StartedAt = jsonx.TimePtr(started)
		it.CreatedAt = jsonx.NewTime(created)
		it.UpdatedAt = jsonx.NewTime(updated)
		slices.SortFunc(it.ActiveJobTypes, func(a, b string) int {
			return slices.Index(jobTypeOrder, a) - slices.Index(jobTypeOrder, b)
		})
		it.FailedStage = failedStage(it.PipelineState, momStatus)
		items = append(items, it)
		sortAts = append(sortAts, sortAt)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	var next *Cursor
	if len(items) > lq.Limit {
		items = items[:lq.Limit]
		next = &Cursor{SortAt: sortAts[lq.Limit-1], ID: items[lq.Limit-1].ID}
	}
	return items, next, nil
}

func failedStage(state string, momStatus *string) *string {
	var s string
	switch {
	case state == StateTranscriptionFailed:
		s = "transcription"
	case state == StateNotesFailed:
		s = "notes"
	case momStatus != nil && *momStatus == "failed":
		s = "mom"
	default:
		return nil
	}
	return &s
}
