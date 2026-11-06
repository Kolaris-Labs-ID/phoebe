package meetings

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"phoebe/internal/apperr"
	"phoebe/internal/config"
	"phoebe/internal/db"
	"phoebe/internal/ids"
	"phoebe/internal/jobs"
	"phoebe/internal/jsonx"
	"phoebe/internal/storage"
)

const (
	MaxTitle        = 200
	MaxParticipants = 100
	MaxParticipant  = 100
	MaxTags         = 20
	MaxQuery        = 200
)

type Service struct {
	pool    *pgxpool.Pool
	cfg     *config.Config
	storage storage.Storage
	log     *slog.Logger
}

func New(pool *pgxpool.Pool, cfg *config.Config, store storage.Storage, log *slog.Logger) *Service {
	return &Service{pool: pool, cfg: cfg, storage: store, log: log}
}

type CreateInput struct {
	ID           *string     `json:"id"`
	Source       *string     `json:"source"`
	Title        *string     `json:"title"`
	StartedAt    *jsonx.Time `json:"started_at"`
	Timezone     *string     `json:"timezone"`
	Language     *string     `json:"language"`
	Participants []string    `json:"participants"`
	FolderID     *string     `json:"folder_id"`
	TagIDs       []string    `json:"tag_ids"`
}

type Patch struct {
	ExpectedMetadataRevision jsonx.Opt[int]        `json:"expected_metadata_revision"`
	Title                    jsonx.Opt[string]     `json:"title"`
	StartedAt                jsonx.Opt[jsonx.Time] `json:"started_at"`
	Timezone                 jsonx.Opt[string]     `json:"timezone"`
	Language                 jsonx.Opt[string]     `json:"language"`
	Participants             jsonx.Opt[[]string]   `json:"participants"`
	FolderID                 jsonx.Opt[string]     `json:"folder_id"`
	TagIDs                   jsonx.Opt[[]string]   `json:"tag_ids"`
}

type fieldErrs []apperr.FieldError

func (f *fieldErrs) add(field, reason string) {
	*f = append(*f, apperr.FieldError{Field: field, Reason: reason})
}

func (f fieldErrs) err() error {
	if len(f) == 0 {
		return nil
	}
	return apperr.Validation(f...)
}

func validTimezone(tz string) bool {
	if tz == "" || len(tz) > 64 || tz == "Local" {
		return false
	}
	_, err := time.LoadLocation(tz)
	return err == nil
}

func normTitle(fe *fieldErrs, s string) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > MaxTitle {
		fe.add("title", apperr.ReasonTooLong)
	}
	return s
}

func normParticipants(fe *fieldErrs, ps []string) []string {
	if len(ps) > MaxParticipants {
		fe.add("participants", apperr.ReasonTooLong)
		return nil
	}
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		p = strings.TrimSpace(p)
		switch {
		case p == "":
			fe.add("participants", apperr.ReasonTooShort)
			return nil
		case utf8.RuneCountInString(p) > MaxParticipant:
			fe.add("participants", apperr.ReasonTooLong)
			return nil
		}
		out = append(out, p)
	}
	return out
}

func normFolderID(fe *fieldErrs, s string) *string {
	id, ok := ids.Parse(s)
	if !ok {
		fe.add("folder_id", apperr.ReasonInvalid)
		return nil
	}
	return &id
}

func normTagIDs(fe *fieldErrs, in []string) []string {
	out := []string{}
	for _, s := range in {
		id, ok := ids.Parse(s)
		if !ok {
			fe.add("tag_ids", apperr.ReasonInvalid)
			return nil
		}
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	if len(out) > MaxTags {
		fe.add("tag_ids", apperr.ReasonTooLong)
		return nil
	}
	slices.Sort(out)
	return out
}

// checkOrganization verifies folder and tags are owned by the user (422 reason not_found).
func checkOrganization(ctx context.Context, q db.Querier, userID string, folderID *string, tagIDs []string) error {
	var fe fieldErrs
	if folderID != nil {
		var ok bool
		if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM folders WHERE id = $1 AND user_id = $2)`,
			*folderID, userID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			fe.add("folder_id", apperr.ReasonNotFound)
		}
	}
	if len(tagIDs) > 0 {
		var n int
		if err := q.QueryRow(ctx, `SELECT count(*) FROM tags WHERE user_id = $1 AND id = ANY($2::uuid[])`,
			userID, tagIDs).Scan(&n); err != nil {
			return err
		}
		if n != len(tagIDs) {
			fe.add("tag_ids", apperr.ReasonNotFound)
		}
	}
	return fe.err()
}

func orgFKError(err error) error {
	if db.IsForeignKeyViolation(err, "") {
		if strings.Contains(db.ConstraintName(err), "folder") {
			return apperr.Field("folder_id", apperr.ReasonNotFound)
		}
		return apperr.Field("tag_ids", apperr.ReasonNotFound)
	}
	return err
}

func setTags(ctx context.Context, q db.Querier, meetingID, userID string, tagIDs []string) error {
	if _, err := q.Exec(ctx, `DELETE FROM meeting_tags WHERE meeting_id = $1`, meetingID); err != nil {
		return err
	}
	if len(tagIDs) == 0 {
		return nil
	}
	_, err := q.Exec(ctx, `INSERT INTO meeting_tags (meeting_id, tag_id, user_id) SELECT $1, t, $2 FROM unnest($3::uuid[]) t`,
		meetingID, userID, tagIDs)
	return err
}

// claimID resolves a client-supplied meeting id: live meeting of this owner → existing
// (replay); another owner's or ever deleted → meeting_id_conflict; unused → false.
func claimID(ctx context.Context, q db.Querier, userID, id string) (existing bool, err error) {
	var owner string
	var deleted bool
	err = q.QueryRow(ctx, `SELECT user_id, deleted_at IS NOT NULL FROM meetings WHERE id = $1`, id).Scan(&owner, &deleted)
	if err == nil {
		if owner == userID && !deleted {
			return true, nil
		}
		return false, apperr.New(apperr.MeetingIDConflict)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	var gone bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM deleted_meetings WHERE id = $1)`, id).Scan(&gone); err != nil {
		return false, err
	}
	if gone {
		return false, apperr.New(apperr.MeetingIDConflict)
	}
	return false, nil
}

// Create implements POST /v1/meetings. created=false: the id already belonged to this
// owner and the existing meeting is returned (body ignored).
func (s *Service) Create(ctx context.Context, userID string, in CreateInput) (*Meeting, bool, error) {
	var fe fieldErrs
	id := ids.New()
	if in.ID != nil {
		v, ok := ids.Parse(*in.ID)
		if !ok {
			return nil, false, apperr.Field("id", apperr.ReasonInvalid)
		}
		id = v
		existing, err := claimID(ctx, s.pool, userID, id)
		if err != nil {
			return nil, false, err
		}
		if existing {
			m, err := s.Get(ctx, userID, id)
			return m, false, err
		}
	}
	source := "recording"
	if in.Source != nil {
		source = *in.Source
		if source != "recording" && source != "import" {
			fe.add("source", apperr.ReasonInvalid)
		}
	}
	title := ""
	if in.Title != nil {
		title = normTitle(&fe, *in.Title)
	}
	var startedAt *time.Time
	if in.StartedAt != nil {
		startedAt = &in.StartedAt.Time
	}
	tz := ""
	if in.Timezone == nil {
		fe.add("timezone", apperr.ReasonRequired)
	} else if tz = *in.Timezone; !validTimezone(tz) {
		fe.add("timezone", apperr.ReasonInvalid)
	}
	lang := "auto"
	if in.Language != nil {
		lang = *in.Language
		if lang != "auto" && lang != "id" && lang != "en" {
			fe.add("language", apperr.ReasonInvalid)
		}
	}
	participants := normParticipants(&fe, in.Participants)
	var folderID *string
	if in.FolderID != nil {
		folderID = normFolderID(&fe, *in.FolderID)
	}
	tagIDs := normTagIDs(&fe, in.TagIDs)
	if err := fe.err(); err != nil {
		return nil, false, err
	}

	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := checkOrganization(ctx, tx, userID, folderID, tagIDs); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO meetings (id, user_id, source, title, started_at, timezone, language, participants, folder_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) ON CONFLICT (id) DO NOTHING`,
			id, userID, source, title, startedAt, tz, lang, participants, folderID)
		if err != nil {
			return orgFKError(err)
		}
		if tag.RowsAffected() == 0 {
			return errIDTaken
		}
		return orgFKError(setTags(ctx, tx, id, userID, tagIDs))
	})
	if errors.Is(err, errIDTaken) {
		existing, err := claimID(ctx, s.pool, userID, id)
		if err != nil {
			return nil, false, err
		}
		if !existing {
			return nil, false, apperr.New(apperr.MeetingIDConflict)
		}
		m, err := s.Get(ctx, userID, id)
		return m, false, err
	}
	if err != nil {
		return nil, false, err
	}
	m, err := s.Get(ctx, userID, id)
	return m, true, err
}

var errIDTaken = errors.New("meetings: id taken")

const meetingCols = `m.id, m.source, m.title, m.started_at, m.timezone, m.language, m.participants,
	m.metadata_revision, m.folder_id, m.pipeline_state, m.created_at, m.updated_at`

func scanMeeting(row pgx.Row) (*Meeting, error) {
	var m Meeting
	var started *time.Time
	var created, updated time.Time
	err := row.Scan(&m.ID, &m.Source, &m.Title, &started, &m.Timezone, &m.Language, &m.Participants,
		&m.MetadataRevision, &m.FolderID, &m.PipelineState, &created, &updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if m.Participants == nil {
		m.Participants = []string{}
	}
	m.StartedAt = jsonx.TimePtr(started)
	m.CreatedAt = jsonx.NewTime(created)
	m.UpdatedAt = jsonx.NewTime(updated)
	return &m, nil
}

func loadTagIDs(ctx context.Context, q db.Querier, meetingID string) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT tag_id FROM meeting_tags WHERE meeting_id = $1 ORDER BY tag_id`, meetingID)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if out == nil {
		out = []string{}
	}
	return out, err
}

// Load assembles the full api.md §4.2 meeting object for an owned, live meeting.
func Load(ctx context.Context, q db.Querier, cfg *config.Config, userID, meetingID string) (*Meeting, error) {
	m, err := scanMeeting(q.QueryRow(ctx,
		`SELECT `+meetingCols+` FROM meetings m WHERE m.id = $1 AND m.user_id = $2 AND m.deleted_at IS NULL`,
		meetingID, userID))
	if err != nil {
		return nil, err
	}
	if m.TagIDs, err = loadTagIDs(ctx, q, meetingID); err != nil {
		return nil, err
	}
	if m.Audio, err = LoadAudio(ctx, q, meetingID); err != nil {
		return nil, err
	}
	if m.Upload, err = LoadLiveUpload(ctx, q, meetingID, cfg.UploadChunkMaxBytes); err != nil {
		return nil, err
	}
	if m.Transcript, err = loadTranscriptRef(ctx, q, meetingID); err != nil {
		return nil, err
	}
	if m.Documents, err = LoadDocumentSummaries(ctx, q, meetingID); err != nil {
		return nil, err
	}
	if m.Jobs.Transcribe, err = LatestJob(ctx, q, meetingID, jobs.TypeTranscribe); err != nil {
		return nil, err
	}
	if m.Jobs.GenerateNotes, err = LatestJob(ctx, q, meetingID, jobs.TypeGenerateNotes); err != nil {
		return nil, err
	}
	if m.Jobs.GenerateMoM, err = LatestJob(ctx, q, meetingID, jobs.TypeGenerateMoM); err != nil {
		return nil, err
	}
	return m, nil
}

// Get reads the meeting object from one snapshot.
func (s *Service) Get(ctx context.Context, userID, meetingID string) (*Meeting, error) {
	var m *Meeting
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(tx pgx.Tx) error {
			var err error
			m, err = Load(ctx, tx, s.cfg, userID, meetingID)
			return err
		})
	return m, err
}

// Update implements PATCH /v1/meetings/{id} (backend.md §10: metadata is revision-checked,
// folder/tags are last-write-wins).
func (s *Service) Update(ctx context.Context, userID, meetingID string, p Patch) (*Meeting, error) {
	var fe fieldErrs
	var title, tz, lang *string
	var participants []string
	if p.Title.Set {
		if p.Title.Null {
			fe.add("title", apperr.ReasonInvalid)
		} else {
			v := normTitle(&fe, p.Title.Value)
			title = &v
		}
	}
	if p.Timezone.Set && (p.Timezone.Null || !validTimezone(p.Timezone.Value)) {
		fe.add("timezone", apperr.ReasonInvalid)
	} else if p.Timezone.Set {
		tz = &p.Timezone.Value
	}
	if p.Language.Set {
		if p.Language.Null || (p.Language.Value != "auto" && p.Language.Value != "id" && p.Language.Value != "en") {
			fe.add("language", apperr.ReasonInvalid)
		} else {
			lang = &p.Language.Value
		}
	}
	if p.Participants.Set {
		if p.Participants.Null {
			fe.add("participants", apperr.ReasonInvalid)
		} else {
			participants = normParticipants(&fe, p.Participants.Value)
		}
	}
	var folderID *string
	if p.FolderID.Set && !p.FolderID.Null {
		folderID = normFolderID(&fe, p.FolderID.Value)
	}
	var tagIDs []string
	if p.TagIDs.Set {
		if p.TagIDs.Null {
			fe.add("tag_ids", apperr.ReasonInvalid)
		} else {
			tagIDs = normTagIDs(&fe, p.TagIDs.Value)
		}
	}
	if err := fe.err(); err != nil {
		return nil, err
	}
	metadataSet := p.Title.Set || p.StartedAt.Set || p.Timezone.Set || p.Language.Set || p.Participants.Set

	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := db.LockMeeting(ctx, tx, meetingID, userID); err != nil {
			return err
		}
		cur, err := scanMeeting(tx.QueryRow(ctx, `SELECT `+meetingCols+` FROM meetings m WHERE m.id = $1`, meetingID))
		if err != nil {
			return err
		}
		if metadataSet && p.ExpectedMetadataRevision.Set && !p.ExpectedMetadataRevision.Null &&
			p.ExpectedMetadataRevision.Value != cur.MetadataRevision {
			return apperr.WithDetails(apperr.MetadataConflict, map[string]any{"current_metadata_revision": cur.MetadataRevision})
		}
		if err := checkOrganization(ctx, tx, userID, folderID, tagIDs); err != nil {
			return err
		}

		next := *cur
		if title != nil {
			next.Title = *title
		}
		if p.StartedAt.Set {
			next.StartedAt = nil
			if !p.StartedAt.Null {
				v := p.StartedAt.Value
				next.StartedAt = &v
			}
		}
		if tz != nil {
			next.Timezone = *tz
		}
		if lang != nil {
			next.Language = *lang
		}
		if p.Participants.Set {
			next.Participants = participants
		}
		if p.FolderID.Set {
			next.FolderID = folderID
		}
		metaChanged := next.Title != cur.Title || !sameTime(next.StartedAt, cur.StartedAt) ||
			next.Timezone != cur.Timezone || next.Language != cur.Language ||
			!slices.Equal(next.Participants, cur.Participants)
		folderChanged := !sameString(next.FolderID, cur.FolderID)
		tagsChanged := false
		if p.TagIDs.Set {
			curTags, err := loadTagIDs(ctx, tx, meetingID)
			if err != nil {
				return err
			}
			tagsChanged = !slices.Equal(curTags, tagIDs)
		}
		if !metaChanged && !folderChanged && !tagsChanged {
			return nil
		}
		inc := 0
		if metaChanged {
			inc = 1
		}
		var started *time.Time
		if next.StartedAt != nil {
			started = &next.StartedAt.Time
		}
		if _, err := tx.Exec(ctx, `
			UPDATE meetings SET title = $2, started_at = $3, timezone = $4, language = $5, participants = $6,
			       folder_id = $7, metadata_revision = metadata_revision + $8, updated_at = now()
			WHERE id = $1`,
			meetingID, next.Title, started, next.Timezone, next.Language, next.Participants, next.FolderID, inc); err != nil {
			return orgFKError(err)
		}
		if tagsChanged {
			return orgFKError(setTags(ctx, tx, meetingID, userID, tagIDs))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, userID, meetingID)
}

func sameTime(a, b *jsonx.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Time.Equal(b.Time)
}

func sameString(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
