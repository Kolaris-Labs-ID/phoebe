package documents

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"phoebe/internal/ai"
	"phoebe/internal/apperr"
	"phoebe/internal/config"
	"phoebe/internal/db"
	"phoebe/internal/idem"
	"phoebe/internal/ids"
	"phoebe/internal/jsonx"
	"phoebe/internal/meetings"
	"phoebe/internal/usage"
)

const (
	OriginAIGenerated = "ai_generated"
	OriginUserEdit    = "user_edit"
	OriginRestore     = "restore"
)

// Service serves the document endpoints (api.md §4.7) and the generate_notes /
// generate_mom job handlers. llm may be nil in the API process.
type Service struct {
	pool  *pgxpool.Pool
	cfg   *config.Config
	usage *usage.Service
	llm   ai.LLM
	log   *slog.Logger
}

func New(pool *pgxpool.Pool, cfg *config.Config, use *usage.Service, llm ai.LLM, log *slog.Logger) *Service {
	return &Service{pool: pool, cfg: cfg, usage: use, llm: llm, log: log}
}

type Source struct {
	TranscriptRevisionID      *string `json:"transcript_revision_id"`
	TranscriptRevisionNumber  *int    `json:"transcript_revision_number"`
	ManualNotesRevisionID     *string `json:"manual_notes_revision_id"`
	ManualNotesRevisionNumber *int    `json:"manual_notes_revision_number"`
	AINotesRevisionID         *string `json:"ai_notes_revision_id"`
	AINotesRevisionNumber     *int    `json:"ai_notes_revision_number"`
}

// Revision is the api.md DocumentRevision; listings leave Content empty (omitted).
type Revision struct {
	ID                         string          `json:"id"`
	DocumentID                 string          `json:"document_id"`
	Kind                       Kind            `json:"kind"`
	RevisionNumber             int             `json:"revision_number"`
	Origin                     string          `json:"origin"`
	RestoredFromRevisionNumber *int            `json:"restored_from_revision_number"`
	ReviewedAt                 *jsonx.Time     `json:"reviewed_at"`
	CreatedAt                  jsonx.Time      `json:"created_at"`
	Source                     Source          `json:"source"`
	Content                    json.RawMessage `json:"content,omitempty"`
}

type View struct {
	Document *meetings.DocumentSummary `json:"document"`
	Revision *Revision                 `json:"revision"`
}

type PutInput struct {
	ExpectedRevision *int            `json:"expected_revision"`
	BaseRevision     *int            `json:"base_revision"`
	MutationID       *string         `json:"mutation_id"`
	Content          json.RawMessage `json:"content"`
}

type RestoreInput struct {
	RevisionNumber   *int    `json:"revision_number"`
	ExpectedRevision *int    `json:"expected_revision"`
	MutationID       *string `json:"mutation_id"`
}

type ReviewInput struct {
	Status           *string `json:"status"`
	ExpectedRevision *int    `json:"expected_revision"`
}

const revisionCols = `r.id, r.document_id, d.kind, r.revision_number, r.origin, rr.revision_number, r.reviewed_at,
	r.created_at, r.source_transcript_revision_id, t.revision_number, r.source_manual_notes_revision_id,
	mn.revision_number, r.source_ai_notes_revision_id, an.revision_number`

const revisionFrom = ` FROM document_revisions r
	JOIN documents d ON d.id = r.document_id
	LEFT JOIN document_revisions rr ON rr.id = r.restored_from_revision_id
	LEFT JOIN transcript_revisions t ON t.id = r.source_transcript_revision_id
	LEFT JOIN document_revisions mn ON mn.id = r.source_manual_notes_revision_id
	LEFT JOIN document_revisions an ON an.id = r.source_ai_notes_revision_id `

func scanRevision(row pgx.Row, withContent bool) (*Revision, error) {
	var r Revision
	var reviewed *time.Time
	var created time.Time
	dst := []any{&r.ID, &r.DocumentID, &r.Kind, &r.RevisionNumber, &r.Origin, &r.RestoredFromRevisionNumber, &reviewed,
		&created, &r.Source.TranscriptRevisionID, &r.Source.TranscriptRevisionNumber, &r.Source.ManualNotesRevisionID,
		&r.Source.ManualNotesRevisionNumber, &r.Source.AINotesRevisionID, &r.Source.AINotesRevisionNumber}
	var content []byte
	if withContent {
		dst = append(dst, &content)
	}
	if err := row.Scan(dst...); err != nil {
		return nil, err
	}
	r.ReviewedAt = jsonx.TimePtr(reviewed)
	r.CreatedAt = jsonx.NewTime(created)
	if withContent {
		r.Content = content
	}
	return &r, nil
}

func revisionByID(ctx context.Context, q db.Querier, id string) (*Revision, error) {
	return scanRevision(q.QueryRow(ctx, `SELECT `+revisionCols+`, r.content`+revisionFrom+`WHERE r.id = $1`, id), true)
}

func revisionByNumber(ctx context.Context, q db.Querier, meetingID string, kind Kind, n int) (*Revision, error) {
	r, err := scanRevision(q.QueryRow(ctx, `SELECT `+revisionCols+`, r.content`+revisionFrom+
		`WHERE d.meeting_id = $1 AND d.kind = $2 AND r.revision_number = $3`, meetingID, kind, n), true)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.ErrNotFound
	}
	return r, err
}

func summary(ctx context.Context, q db.Querier, meetingID string, kind Kind) (*meetings.DocumentSummary, error) {
	docs, err := meetings.LoadDocumentSummaries(ctx, q, meetingID)
	if err != nil {
		return nil, err
	}
	switch kind {
	case KindManualNotes:
		return docs.ManualNotes, nil
	case KindAINotes:
		return docs.AINotes, nil
	default:
		return docs.MoM, nil
	}
}

func ownMeeting(ctx context.Context, q db.Querier, userID, meetingID string) error {
	var one int
	err := q.QueryRow(ctx, `SELECT 1 FROM meetings WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL`,
		meetingID, userID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.ErrNotFound
	}
	return err
}

// readTx gives multi-statement reads one consistent snapshot.
func (s *Service) readTx(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, fn)
}

func view(ctx context.Context, q db.Querier, meetingID string, kind Kind, revisionID string) (*View, error) {
	sum, err := summary(ctx, q, meetingID, kind)
	if err != nil {
		return nil, err
	}
	rev, err := revisionByID(ctx, q, revisionID)
	if err != nil {
		return nil, err
	}
	return &View{Document: sum, Revision: rev}, nil
}

func (s *Service) Get(ctx context.Context, userID, meetingID string, kind Kind) (*View, error) {
	var v *View
	err := s.readTx(ctx, func(tx pgx.Tx) error {
		if err := ownMeeting(ctx, tx, userID, meetingID); err != nil {
			return err
		}
		sum, err := summary(ctx, tx, meetingID, kind)
		if err != nil {
			return err
		}
		if sum == nil {
			return apperr.ErrNotFound
		}
		v, err = view(ctx, tx, meetingID, kind, sum.CurrentRevisionID)
		return err
	})
	return v, err
}

func (s *Service) GetRevision(ctx context.Context, userID, meetingID string, kind Kind, n int) (*Revision, error) {
	if err := ownMeeting(ctx, s.pool, userID, meetingID); err != nil {
		return nil, err
	}
	return revisionByNumber(ctx, s.pool, meetingID, kind, n)
}

// ListRevisions returns revisions newest first without content; before = 0 starts at the
// newest. next is the revision number to continue before, nil on the last page.
func (s *Service) ListRevisions(ctx context.Context, userID, meetingID string, kind Kind, limit, before int) ([]Revision, *int, error) {
	if err := ownMeeting(ctx, s.pool, userID, meetingID); err != nil {
		return nil, nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT `+revisionCols+revisionFrom+`
		WHERE d.meeting_id = $1 AND d.kind = $2 AND ($3 = 0 OR r.revision_number < $3)
		ORDER BY r.revision_number DESC LIMIT $4`, meetingID, kind, before, limit+1)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	out := []Revision{}
	for rows.Next() {
		r, err := scanRevision(rows, false)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if len(out) <= limit {
		return out, nil, nil
	}
	out = out[:limit]
	next := out[limit-1].RevisionNumber
	return out, &next, nil
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

func (f *fieldErrs) revision(field string, v *int, min int) {
	switch {
	case v == nil:
		f.add(field, apperr.ReasonRequired)
	case *v < min:
		f.add(field, apperr.ReasonOutOfRange)
	}
}

func (f *fieldErrs) mutationID(v *string) string {
	if v == nil {
		f.add("mutation_id", apperr.ReasonRequired)
		return ""
	}
	id, ok := ids.Parse(*v)
	if !ok {
		f.add("mutation_id", apperr.ReasonInvalid)
	}
	return id
}

// replay is the §8.2 mutation_id lookup: same fingerprint → that revision (200), even if
// the document has advanced; different fingerprint → idempotency_key_reused.
func replay(ctx context.Context, q db.Querier, meetingID string, kind Kind, mutationID, fingerprint string) (*View, error) {
	var id, fp string
	err := q.QueryRow(ctx, `
		SELECT r.id, r.mutation_fingerprint FROM document_revisions r JOIN documents d ON d.id = r.document_id
		WHERE d.meeting_id = $1 AND d.kind = $2 AND r.mutation_id = $3`, meetingID, kind, mutationID).Scan(&id, &fp)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if fp != fingerprint {
		return nil, apperr.New(apperr.IdempotencyKeyReused)
	}
	return view(ctx, q, meetingID, kind, id)
}

type docRow struct {
	id      string
	current int
}

// lockDocument locks the (meeting, kind) document row; nil when it does not exist yet.
func lockDocument(ctx context.Context, q db.Querier, meetingID string, kind Kind) (*docRow, error) {
	var d docRow
	err := q.QueryRow(ctx, `SELECT id, current_revision_number FROM documents WHERE meeting_id = $1 AND kind = $2 FOR UPDATE`,
		meetingID, kind).Scan(&d.id, &d.current)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &d, err
}

// ensureDocument creates the document row if missing and returns it locked.
func ensureDocument(ctx context.Context, q db.Querier, meetingID string, kind Kind) (*docRow, error) {
	if _, err := q.Exec(ctx, `INSERT INTO documents (meeting_id, kind) VALUES ($1, $2) ON CONFLICT (meeting_id, kind) DO NOTHING`,
		meetingID, kind); err != nil {
		return nil, err
	}
	return lockDocument(ctx, q, meetingID, kind)
}

func checkExpected(ctx context.Context, q db.Querier, d *docRow, expected int) error {
	current := 0
	if d != nil {
		current = d.current
	}
	if current == expected {
		return nil
	}
	var currentID *string
	if d != nil {
		var id string
		if err := q.QueryRow(ctx, `SELECT id FROM document_revisions WHERE document_id = $1 AND revision_number = $2`,
			d.id, d.current).Scan(&id); err != nil {
			return err
		}
		currentID = &id
	}
	return apperr.WithDetails(apperr.RevisionConflict, map[string]any{
		"current_revision_number": current, "current_revision_id": currentID})
}

type sourceIDs struct {
	transcript, manualNotes, aiNotes *string
}

func revisionSources(ctx context.Context, q db.Querier, documentID string, n int) (sourceIDs, error) {
	var s sourceIDs
	err := q.QueryRow(ctx, `
		SELECT source_transcript_revision_id, source_manual_notes_revision_id, source_ai_notes_revision_id
		FROM document_revisions WHERE document_id = $1 AND revision_number = $2`, documentID, n).
		Scan(&s.transcript, &s.manualNotes, &s.aiNotes)
	return s, err
}

func loadSegments(ctx context.Context, q db.Querier, transcriptRevisionID string) ([]segment, error) {
	var raw []byte
	if err := q.QueryRow(ctx, `SELECT segments FROM transcript_revisions WHERE id = $1`, transcriptRevisionID).Scan(&raw); err != nil {
		return nil, err
	}
	var segs []segment
	if err := json.Unmarshal(raw, &segs); err != nil {
		return nil, err
	}
	return segs, nil
}

func loadManualNotes(ctx context.Context, q db.Querier, revisionID string) (*ManualNotes, error) {
	var raw []byte
	if err := q.QueryRow(ctx, `SELECT content FROM document_revisions WHERE id = $1`, revisionID).Scan(&raw); err != nil {
		return nil, err
	}
	var n ManualNotes
	if err := json.Unmarshal(raw, &n); err != nil {
		return nil, err
	}
	return &n, nil
}

// pinsFor builds the ref pins of a revision's sources (nil maps where not pinned).
func pinsFor(ctx context.Context, q db.Querier, src sourceIDs) (Pins, error) {
	var p Pins
	if src.transcript != nil {
		segs, err := loadSegments(ctx, q, *src.transcript)
		if err != nil {
			return p, err
		}
		p.Segments = make(map[string]Span, len(segs))
		for _, g := range segs {
			p.Segments[g.ID] = Span{g.StartMS, g.EndMS}
		}
	}
	if src.manualNotes != nil {
		n, err := loadManualNotes(ctx, q, *src.manualNotes)
		if err != nil {
			return p, err
		}
		p.NoteIDs = make(map[string]bool, len(n.Items))
		for _, it := range n.Items {
			p.NoteIDs[it.ID] = true
		}
	}
	return p, nil
}

type newRevision struct {
	ID             string
	Number         int
	Origin         string
	Content        []byte
	Sources        sourceIDs
	RestoredFromID *string
	JobID          *string
	MutationID     *string
	MutationFP     *string
	Provider       *string
	Model          *string
	PromptVersion  *string
}

// commitRevision appends the revision and makes it current: documents bump, search
// upsert, meeting updated_at (backend.md §10, §3.5). Caller holds meeting + document locks.
func commitRevision(ctx context.Context, q db.Querier, meetingID string, kind Kind, d *docRow, r newRevision, c Content) error {
	if _, err := q.Exec(ctx, `
		INSERT INTO document_revisions (id, document_id, revision_number, origin, schema_version, content,
		    source_transcript_revision_id, source_manual_notes_revision_id, source_ai_notes_revision_id,
		    restored_from_revision_id, job_id, mutation_id, mutation_fingerprint, provider, model, prompt_version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
		r.ID, d.id, r.Number, r.Origin, SchemaVersion, r.Content, r.Sources.transcript, r.Sources.manualNotes,
		r.Sources.aiNotes, r.RestoredFromID, r.JobID, r.MutationID, r.MutationFP, r.Provider, r.Model, r.PromptVersion); err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `UPDATE documents SET current_revision_number = $2, updated_at = now() WHERE id = $1`,
		d.id, r.Number); err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `
		INSERT INTO search_documents (meeting_id, source, body, updated_at) VALUES ($1, $2, $3, now())
		ON CONFLICT (meeting_id, source) DO UPDATE SET body = EXCLUDED.body, updated_at = now()`,
		meetingID, kind, plainText(c)); err != nil {
		return err
	}
	return db.TouchMeeting(ctx, q, meetingID)
}

// Put saves a user edit as a new revision (api.md §4.7, backend.md §10). created=false is
// a mutation_id replay (200).
func (s *Service) Put(ctx context.Context, userID, meetingID string, kind Kind, raw []byte, in PutInput) (*View, bool, error) {
	var fe fieldErrs
	fe.revision("expected_revision", in.ExpectedRevision, 0)
	mutationID := fe.mutationID(in.MutationID)
	if in.BaseRevision != nil && in.ExpectedRevision != nil && (*in.BaseRevision < 0 || *in.BaseRevision > *in.ExpectedRevision) {
		fe.add("base_revision", apperr.ReasonOutOfRange)
	}
	if len(in.Content) == 0 {
		fe.add("content", apperr.ReasonRequired)
	}
	if err := fe.err(); err != nil {
		return nil, false, err
	}
	expected := *in.ExpectedRevision
	base := expected
	if in.BaseRevision != nil {
		base = *in.BaseRevision
	}
	fp, err := idem.MutationFingerprint(raw)
	if err != nil {
		return nil, false, apperr.New(apperr.InvalidJSON)
	}

	var out *View
	created := false
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := db.LockMeeting(ctx, tx, meetingID, userID); err != nil {
			return err
		}
		v, err := replay(ctx, tx, meetingID, kind, mutationID, fp)
		if v != nil || err != nil {
			out = v
			return err
		}
		d, err := lockDocument(ctx, tx, meetingID, kind)
		if err != nil {
			return err
		}
		if err := checkExpected(ctx, tx, d, expected); err != nil {
			return err
		}
		var src sourceIDs
		if base > 0 {
			if src, err = revisionSources(ctx, tx, d.id, base); err != nil {
				return err
			}
		}
		pins, err := pinsFor(ctx, tx, src)
		if err != nil {
			return err
		}
		c, err := ValidateContent(kind, in.Content, pins)
		if err != nil {
			return err
		}
		content, err := json.Marshal(c)
		if err != nil {
			return err
		}
		if d == nil {
			if d, err = ensureDocument(ctx, tx, meetingID, kind); err != nil {
				return err
			}
		}
		id := ids.New()
		if err := commitRevision(ctx, tx, meetingID, kind, d, newRevision{ID: id, Number: d.current + 1,
			Origin: OriginUserEdit, Content: content, Sources: src, MutationID: &mutationID, MutationFP: &fp}, c); err != nil {
			return err
		}
		out, err = view(ctx, tx, meetingID, kind, id)
		created = true
		return err
	})
	if err != nil {
		return nil, false, err
	}
	return out, created, nil
}

// Restore appends a copy (content + sources) of an earlier revision (origin restore).
func (s *Service) Restore(ctx context.Context, userID, meetingID string, kind Kind, raw []byte, in RestoreInput) (*View, bool, error) {
	var fe fieldErrs
	fe.revision("revision_number", in.RevisionNumber, 1)
	fe.revision("expected_revision", in.ExpectedRevision, 0)
	mutationID := fe.mutationID(in.MutationID)
	if err := fe.err(); err != nil {
		return nil, false, err
	}
	fp, err := idem.MutationFingerprint(raw)
	if err != nil {
		return nil, false, apperr.New(apperr.InvalidJSON)
	}

	var out *View
	created := false
	err = db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := db.LockMeeting(ctx, tx, meetingID, userID); err != nil {
			return err
		}
		v, err := replay(ctx, tx, meetingID, kind, mutationID, fp)
		if v != nil || err != nil {
			out = v
			return err
		}
		d, err := lockDocument(ctx, tx, meetingID, kind)
		if err != nil {
			return err
		}
		if d == nil {
			return apperr.ErrNotFound
		}
		if err := checkExpected(ctx, tx, d, *in.ExpectedRevision); err != nil {
			return err
		}
		var oldID string
		var content []byte
		var src sourceIDs
		err = tx.QueryRow(ctx, `
			SELECT id, content, source_transcript_revision_id, source_manual_notes_revision_id, source_ai_notes_revision_id
			FROM document_revisions WHERE document_id = $1 AND revision_number = $2`, d.id, *in.RevisionNumber).
			Scan(&oldID, &content, &src.transcript, &src.manualNotes, &src.aiNotes)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.ErrNotFound
		}
		if err != nil {
			return err
		}
		c, err := DecodeContent(kind, content)
		if err != nil {
			return err
		}
		id := ids.New()
		if err := commitRevision(ctx, tx, meetingID, kind, d, newRevision{ID: id, Number: d.current + 1,
			Origin: OriginRestore, Content: content, Sources: src, RestoredFromID: &oldID,
			MutationID: &mutationID, MutationFP: &fp}, c); err != nil {
			return err
		}
		out, err = view(ctx, tx, meetingID, kind, id)
		created = true
		return err
	})
	if err != nil {
		return nil, false, err
	}
	return out, created, nil
}

// Review sets (reviewed) or clears (draft) reviewed_at on the current revision; the
// caller must name the revision it reviewed (expected_revision).
func (s *Service) Review(ctx context.Context, userID, meetingID string, kind Kind, in ReviewInput) (*meetings.DocumentSummary, error) {
	var fe fieldErrs
	switch {
	case in.Status == nil:
		fe.add("status", apperr.ReasonRequired)
	case *in.Status != "reviewed" && *in.Status != "draft":
		fe.add("status", apperr.ReasonInvalid)
	}
	fe.revision("expected_revision", in.ExpectedRevision, 1)
	if err := fe.err(); err != nil {
		return nil, err
	}
	var out *meetings.DocumentSummary
	err := db.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := db.LockMeeting(ctx, tx, meetingID, userID); err != nil {
			return err
		}
		d, err := lockDocument(ctx, tx, meetingID, kind)
		if err != nil {
			return err
		}
		if d == nil || d.current == 0 {
			return apperr.ErrNotFound
		}
		if err := checkExpected(ctx, tx, d, *in.ExpectedRevision); err != nil {
			return err
		}
		set := `reviewed_at = now() WHERE document_id = $1 AND revision_number = $2 AND reviewed_at IS NULL`
		if *in.Status == "draft" {
			set = `reviewed_at = NULL WHERE document_id = $1 AND revision_number = $2 AND reviewed_at IS NOT NULL`
		}
		tag, err := tx.Exec(ctx, `UPDATE document_revisions SET `+set, d.id, d.current)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 1 {
			if _, err := tx.Exec(ctx, `UPDATE documents SET updated_at = now() WHERE id = $1`, d.id); err != nil {
				return err
			}
			if err := db.TouchMeeting(ctx, tx, meetingID); err != nil {
				return err
			}
		}
		out, err = summary(ctx, tx, meetingID, kind)
		return err
	})
	return out, err
}

// plainText is the search_documents body of a revision (backend.md §10).
func plainText(c Content) string {
	var parts []string
	add := func(s ...string) {
		for _, x := range s {
			if x != "" {
				parts = append(parts, x)
			}
		}
	}
	texts := func(items []TextItem) {
		for _, it := range items {
			add(it.Text)
		}
	}
	switch c := c.(type) {
	case *ManualNotes:
		for _, it := range c.Items {
			add(it.Text)
		}
	case *AINotes:
		for _, sec := range c.Sections {
			add(sec.Heading)
			texts(sec.Items)
		}
	case *MoM:
		add(c.MeetingInfo.Title)
		add(c.MeetingInfo.Participants...)
		texts(c.MeetingInfo.Agenda)
		for _, sec := range c.Sections {
			add(sec.Heading)
			texts(sec.Items)
			for _, t := range sec.Topics {
				add(t.Title)
				texts(t.Points)
			}
			for _, a := range sec.Actions {
				add(a.Description)
				if a.PIC != nil {
					add(*a.PIC)
				}
				if a.DeadlinePhrase != nil {
					add(*a.DeadlinePhrase)
				}
			}
		}
	}
	return strings.Join(parts, "\n")
}
