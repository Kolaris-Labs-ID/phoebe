# Foundation package APIs

Contract for domain agents. Spec: `docs/backend.md`, `docs/api.md`. All ids are lowercase UUID **strings** in Go (pgx encodes/decodes `uuid` ↔ `string`); nullable columns scan into pointers.

## apperr — domain error codes (no HTTP)

```go
type Code string                       // one const per api.md §1.2 code: apperr.NotFound, apperr.RevisionConflict, …
type Error struct { Code Code; Details map[string]any }   // Details never carry content
var ErrNotFound *Error                 // errors.Is(err, apperr.ErrNotFound) matches ANY not_found error (Is compares Code)
func New(code Code) *Error
func WithDetails(code Code, details map[string]any) *Error   // e.g. revision_conflict {current_revision_number, current_revision_id}
func Validation(fields ...FieldError) *Error                  // 422 validation_failed {"fields":[{field,reason}]}
func Field(field, reason string) *Error                       // single-field shortcut
func Content(path, reason string) *Error                      // 422 invalid_content {path (JSON pointer), reason}
func RateLimit(retryAfter time.Duration) *Error               // 429 + Retry-After header (ceil seconds, ≥1)
func CodeOf(err error) Code; func Is(err error, code Code) bool
type FieldError struct { Field, Reason string }
const ReasonRequired, ReasonTooLong, ReasonTooShort, ReasonInvalid, ReasonNotFound, ReasonDuplicate, ReasonOutOfRange
```
Domain services return `*apperr.Error` for every client-visible failure. Any other error → 500 `internal` (or 503 `unavailable` if `db.IsUnavailable`).

## ids / idem / jsonx

```go
ids.New() string                          // UUIDv4, lowercase
ids.Parse(s) (lower string, ok bool)      // any-case canonical 8-4-4-4-12
ids.Valid(s) bool

idem.CanonicalJSON(body []byte) ([]byte, error)          // keys sorted, numbers as received, empty body = {}
idem.RequestFingerprint(routePattern, meetingID string, body []byte) (string, error)  // backend.md §8; route e.g. "POST /v1/meetings/{id}/transcription"
idem.MutationFingerprint(body []byte) (string, error)    // backend.md §8.2

jsonx.Time            // MarshalJSON "2026-11-06T02:00:00.000Z" (UTC, always .mmm); Unmarshal any RFC3339 → UTC, ms-truncated
jsonx.NewTime(t) Time; jsonx.TimePtr(*time.Time) *Time   // nullable: *jsonx.Time encodes null
jsonx.FormatTime(t) string; const jsonx.TimeLayout
jsonx.Opt[T]{Set, Null bool; Value T}   // PATCH tri-state: absent (Set=false) / null (Null) / value; also "nullable key must be present" checks
jsonx.Decode(b []byte, v any) error      // strict syntax/types, unknown fields ignored, no trailing data
```

## obs

```go
obs.NewLogger(w io.Writer, level slog.Leveler) *slog.Logger  // JSON; *Context calls stamp correlation_id + user_id from ctx
obs.WithCorrelationID(ctx, id) / obs.CorrelationID(ctx) string   // HTTP: = request id (UUID); jobs: jobs.correlation_id
obs.WithUserID(ctx, id) / obs.UserID(ctx) string
```
Always log with `log.InfoContext(ctx, "event.name", allowed-fields…)`. Allowed fields: backend.md §15 only.

## config

```go
config.Load() (*Config, error); config.LoadFrom(getenv func(string) string) (*Config, error)
(*Config).AppleConfigured() bool; (*Config).CheckAPIStartup() error; (*Config).String() string  // redacted
```
Fields mirror §16 in CamelCase: `DatabaseURL, HTTPAddr, PublicBaseURL, SigningSecret []byte, SubjectPepper []byte, SignedURLTTL, StorageDir, AppleBundleID, AppleJWKSURL, AppleTokenURL, AppleRevokeURL, AppleTeamID, AppleKeyID, ApplePrivateKey, TokenEncKey []byte (32), DevLoginEnabled, SessionTTL, MaxAudioSeconds int64, MaxUploadBytes int64, UploadChunkMaxBytes int64, UploadSessionTTL, UploadWriterLease, AudioRetentionDays int, TrialQuotaMinutes int64, TrialPeriodDays int, AIUserJobsPerDay int, PurgeDelay, STTProvider, STTBaseURL, STTAPIKey, STTModel, STTRequestTimeout, STTMaxFileBytes int64, STTChunkSeconds int, STTPricePerMin float64, FFmpegPath, LLMProvider, LLMBaseURL, LLMAPIKey, LLMModel, LLMTemperature float64, LLMRequestTimeout, LLMMaxTranscriptChars int, LLMPriceInPerMTok, LLMPriceOutPerMTok float64, WorkerID, WorkerConcurrency int, WorkerPollInterval, WorkerShutdownGrace, JobLease, JobTimeoutTranscribeMax, JobTimeoutGenerateMax, JobRetryBase, SweepInterval, AlertQueueAge, AlertDailyCostUSD float64, LogLevel slog.Level`. Durations are `time.Duration`. Secrets (hex or base64) are decoded bytes.

## db

```go
type Querier interface { Exec; Query; QueryRow }   // *pgxpool.Pool, pgx.Tx, *pgx.Conn
type TxBeginner interface { Begin(ctx) (pgx.Tx, error) }
db.Open(ctx, url) (*pgxpool.Pool, error)           // pings
db.Migrate(ctx, pool) error                        // embedded migrations/*.sql, advisory lock 7271001
db.InTx(ctx, b TxBeginner, fn func(pgx.Tx) error) error   // commit on nil, rollback on error/panic
db.IsUniqueViolation(err, constraint string) bool  // "" = any; unique INDEX names count (e.g. "jobs_one_active_per_meeting_key")
db.IsForeignKeyViolation(err, constraint) bool; db.IsCheckViolation(err, constraint) bool; db.ConstraintName(err) string
db.IsUnavailable(err) bool                         // connect/08xxx/admin shutdown → 503

// Meeting lock (backend.md §2) — FIRST statement of every write tx on a meeting or its children:
db.LockMeeting(ctx, q, meetingID, userID string) error       // owner + deleted_at IS NULL, FOR UPDATE; miss → apperr.ErrNotFound
db.LockLiveMeeting(ctx, q, meetingID) (userID string, err)   // worker finalize (no owner filter); miss → apperr.ErrNotFound
db.TryLockLiveMeeting(ctx, q, meetingID) (ok bool, err)      // sweeps: FOR UPDATE SKIP LOCKED; false = skip item
db.TouchMeeting(ctx, q, meetingID) error                     // meetings.updated_at = now() (§3.5)
```
Lock order: meeting → upload session → document → job → entitlement → quota reservation. No network/storage I/O inside a tx.

## storage

```go
type Storage interface {
    Put(ctx, key string, r io.Reader) (size int64, err error)              // atomic temp+rename
    Append(ctx, key string, offset int64, r io.Reader,
           renew func(written int64) error, commit func(written int64) error) (written int64, err error)
    Open(ctx, key string) (io.ReadSeekCloser, int64, error)                // missing → ErrNotFound
    Stat(ctx, key string) (size int64, exists bool, err error)
    Move(ctx, src, dst string) error                                       // atomic, overwrites; missing src → ErrNotFound
    Delete(ctx, key string) error                                          // missing = nil
    DeletePrefix(ctx, prefix string) error                                 // prefix must end "/"; missing = nil
    Walk(ctx, fn func(key string, modTime time.Time) error) error          // every object (orphan sweep); vanished entries skipped
}
var ErrNotFound, ErrBusy, ErrInvalidKey, ErrShort error
const RenewEvery = 4 << 20
storage.NewLocalFS(root string) (*LocalFS, error)   // files 0600, dirs 0700; rejects "", abs, "..", ".", "//", "\\"
storage.NewSigner(secret []byte, publicBaseURL string, ttl time.Duration) *Signer
(*Signer).Sign(kind, id string, now time.Time) (url string, expiresAt time.Time)   // kind "audio"|"export"
(*Signer).Verify(kind, id, exp, sig string, now time.Time) error                 // apperr invalid_signature, then url_expired
```
**Append** (deviation from §12's signature, needed for §7.2 "commit while still holding the flock"): non-blocking `flock` (held → `ErrBusy`), file shorter than offset → `ErrShort`, truncate to offset, stream `r` (caller wraps it in `io.LimitReader(body, limit+1)` and treats `written > limit` as `chunk_too_large`/`upload_too_large` inside `commit` by not committing), `renew` after every 4 MiB (error → stop, no commit, returned), fsync, then `commit(written)` under the lock — also after a body read error / ctx cancel, so persisted bytes count. Use `context.WithoutCancel` for DB work inside `commit`. Returned err precedence: lock/open/truncate, renew, commit, read/write.

Keys: `u/<uid>/m/<mid>/uploads/<session>.part`, `u/<uid>/m/<mid>/audio/<session>.<ext>`, `u/<uid>/m/<mid>/exports/<export>.<ext>`. The orphan sweep (`meetings.OrphanSweep`) deletes any object older than 24 h that does not match one of these and is not referenced by its row, so new key layouts must be added to `meetings.referenced`.

## jobs

```go
consts: TypeTranscribe, TypeGenerateNotes, TypeGenerateMoM, TypePurgeMeeting, TypePurgeAccount
        TriggerUser, TriggerSystem; StatusQueued/Running/Succeeded/Failed/Canceled
        CatProviderUnavailable … CatInternal (all last_error_category values); AIMaxAttempts=3, PurgeMaxAttempts=10
type Job struct { ID, Type string; UserID, MeetingID *string; Trigger, Status, IdempotencyKey string;
                  RequestFingerprint *string; Payload, Result json.RawMessage; Attempts, MaxAttempts int; RunAfter time.Time;
                  LeaseOwner *string; LeaseUntil, HeartbeatAt *time.Time; LastErrorCategory, LastErrorDetail *string;
                  CorrelationID string; CreatedAt, UpdatedAt time.Time; StartedAt, FinishedAt *time.Time }
(*Job).Token() Token; (*Job).DecodePayload(v any) error
type Token struct { ID, Owner string; Attempt int }     // fencing (id, lease_owner, attempts)
const Columns   // select list for ScanJob: "SELECT "+jobs.Columns+" FROM jobs j WHERE …" (alias must be j)
func ScanJob(row pgx.Row) (*Job, error)

type NewJob struct { ID, Type, UserID, MeetingID, Trigger, IdempotencyKey, RequestFingerprint string; Payload any;
                     MaxAttempts int; RunAfter time.Time; CorrelationID string }   // "" → NULL / DB default; zero RunAfter = now()
func Enqueue(ctx, q, NewJob) (*Job, error)   // in caller tx; ON CONFLICT DO NOTHING → ErrDuplicate (tx stays usable); bumps meeting
var ErrDuplicate, ErrLeaseLost error
func Get(ctx, q, id) / GetByKey(ctx, q, userID, key) / Active(ctx, q, meetingID, type) / Latest(ctx, q, meetingID, type) (*Job, error)  // pgx.ErrNoRows on miss; no owner check
func Claim(ctx, q, owner string, lease time.Duration) (*Job, error)              // nil,nil when none
func Heartbeat(ctx, q, Token, lease) (ok bool, err error)
func Retry(ctx, q, j, delay, category, detail) (ok bool, err error)
func Succeed(ctx, q, j, result any) (ok bool, err error); func Fail(ctx, q, j, category, detail) (ok bool, err error)
func Release(ctx, q, j) (ok bool, err error)                                     // shutdown requeue, attempts-1
// all fenced; ok=false = lease lost; every status change bumps meetings.updated_at for meeting-scoped jobs

func Complete(ctx, b db.TxBeginner, j *Job, result any, stage func(tx pgx.Tx) error) error
// Success finalize (§4.5): tx = LockLiveMeeting (meeting jobs) → Succeed(fenced) → stage(tx) → commit.
// Meeting gone or fencing lost → ErrLeaseLost (nothing written). Pre-generate ids (ids.New()) that go into result.

func Retryable(category, detail string, retryAfter time.Duration) error; func Permanent(category, detail string) error
type Error struct { Category, Detail string; RetryAfter time.Duration; Permanent bool }

type Handler func(ctx context.Context, j *Job) error   // nil ONLY after a successful Complete
type Spec struct {
    Handler   Handler
    Deadline  func(j *Job) time.Duration    // required; expiry → retryable "timeout"
    Backoff   Backoff                        // zero = AIBackoff(JOB_RETRY_BASE); purge: PurgeBackoff()
    OnFailure func(ctx, tx pgx.Tx, j *Job, category string) error  // permanent-failure stage writes (release quota, pipeline_state)
}
type Backoff struct { Base, Cap time.Duration }; (Backoff).Delay(attempts int, retryAfter time.Duration) time.Duration
func AIBackoff(base) Backoff (cap 5 min); func PurgeBackoff() Backoff (1 min, cap 1 h)
type RunnerConfig struct { WorkerID string; Concurrency int; PollInterval, Lease, ShutdownGrace, RetryBase, SweepInterval time.Duration }
type Pool interface { db.Querier; db.TxBeginner }    // *pgxpool.Pool
func NewRunner(pool Pool, log *slog.Logger, cfg RunnerConfig) *Runner
(*Runner).Register(jobType string, s Spec)
(*Runner).RegisterSweep(name string, fn SweepFunc)   // type SweepFunc func(ctx) (count int, err error)
(*Runner).Run(ctx) error                              // until ctx done; graceful shutdown per §4.6
(*Runner).RunOnce(ctx) (ran bool, err error)          // tests: claim + run one job synchronously
(*Runner).RunSweeps(ctx) error                        // tests: run every sweep once
func AlertSweep(q, log, queueAge time.Duration, dailyCostUSD float64) SweepFunc   // alert.queue_stuck / alert.cost_spike
```
Runner outcome rules: `nil` = done; `ErrLeaseLost` or heartbeat loss (ctx cause `ErrLeaseLost`) = log `job.lease_lost`, write nothing; forced shutdown = `Release`; attempt deadline = retryable `timeout`; `*jobs.Error` as given; other errors/panics = retryable `internal`. Retryable with `attempts < max_attempts` → `Retry` with backoff; otherwise permanent: tx = LockLiveMeeting → `Fail` (fenced) → `OnFailure` → commit. Claimed with `attempts > max_attempts` (expired leases) → straight to permanent failure with `last_error_category` (`timeout`). Handler ctx carries correlation/user ids for logging.

## httpapi

Single route table; each domain owns **one file** `internal/httpapi/routes_<domain>.go` (auth, users, meetings, folders_tags, uploads, transcripts, documents, exports, files, usage, events) containing its service type and `func (s *Server) routes<Domain>() []Route`. The service type (`AuthService`, `UsersService`, `MeetingsService`, `FoldersTagsService`, `UploadsService`, `TranscriptsService`, `DocumentsService`, `ExportsService`, `FilesService`, `UsageService`, `EventsService`) is declared in that file — redefine it as an interface or a type alias (`type MeetingsService = *meetings.Service`). Handlers reach it as `s.Meetings` etc. Do not edit `server.go`; `cmd/api` wiring fills `Deps`.

```go
type Deps struct { Config *config.Config; Log *slog.Logger; DB *pgxpool.Pool; Storage storage.Storage; Signer *storage.Signer;
                   Sessions SessionResolver; Auth AuthService; Users UsersService; Meetings MeetingsService; FoldersTags FoldersTagsService;
                   Uploads UploadsService; Transcripts TranscriptsService; Documents DocumentsService; Exports ExportsService;
                   Files FilesService; Usage UsageService; Events EventsService }
type SessionResolver interface { ResolveSession(ctx, token string) (userID, sessionID string, err error) }  // miss → apperr Unauthenticated
func New(d Deps) *Server            // http.Handler; tests: httptest.NewServer(httpapi.New(deps))
(*Server).Routes() []Route          // for the cross-account test
type Route struct { Method, Path string; Public bool; BodyLimit int64; Handle HandlerFunc }   // Path is a net/http pattern "/v1/meetings/{id}"
(Route).Pattern() string            // "GET /v1/meetings/{id}" — also the logged route
type HandlerFunc func(w http.ResponseWriter, r *http.Request) error   // return errors; never write error bodies yourself
const NoBodyLimit = -1, DefaultBodyLimit = 2 MiB
type Page[T] struct { Items []T `json:"items"`; NextCursor *string `json:"next_cursor"` }; func NewPage[T](items []T, next *string) Page[T]
const DefaultPageLimit = 50, MaxPageLimit = 200
```
Unexported helpers (same package, use from your routes file):
```go
userID(r) string; sessionID(r) string                    // set by auth middleware
decodeJSON(r, &v) (raw []byte, err error)                // empty body = {}; invalid_json / payload_too_large; raw for fingerprints
writeJSON(w, status, v); writeNoContent(w)
pathUUID(r, "id") (string, error)                        // non-UUID → not_found
idempotencyKey(r) (string, error)                        // missing/non-UUID → missing_idempotency_key
pageParams(r, &cursorStruct) (limit int, hasCursor bool, err error)  // limit/cursor validation_failed
encodeCursor(v any) *string                              // opaque base64url JSON keyset cursor
s.writeError(w, r, err)                                  // only for handlers that stream (files) and must write errors directly
```
Middleware order: request id (`X-Request-ID` UUID echoed, else generated; = correlation id) → access log (`http.request` with method, route pattern, http_status, duration_ms, user_id; never path/query/headers/bodies) → recover (500 envelope) → mux → per route: body limit → bearer auth (non-`Public`) → handler. Unknown route/method → JSON 404. `X-Request-ID` value is in `obs.CorrelationID(r.Context())` — pass it as `jobs.NewJob.CorrelationID`.

Implemented here: `GET /healthz` (DB ping → `{"status":"ok"}` / 503) and `GET /v1/config` (api.md §4.0).

## testutil

```go
testutil.DB(t) *pgxpool.Pool                // fresh migrated phoebe_test_<hex>; dropped WITH (FORCE) on cleanup; no DATABASE_URL → t.Fatal
testutil.Storage(t) *storage.LocalFS        // on t.TempDir()
testutil.Config(t) *config.Config           // valid; fake providers; dev login; PUBLIC_BASE_URL http://phoebe.test
testutil.Logger(t) *slog.Logger; testutil.DiscardLogger() *slog.Logger
testutil.CreateUser(t, q, quotaSeconds) User{ID, Token}   // dev user + active trial entitlement + session (token_hash = sha256(token))
testutil.CreateSession(t, q, userID) (token string)
testutil.CreateMeeting(t, q, userID) (meetingID string)  // awaiting_audio, Asia/Jakarta, started_at now
testutil.WAV(durationMS int64) []byte                     // PCM 16-bit mono 8 kHz tone (16 000 B/s + 44 B header)
testutil.SHA256Hex(b []byte) string
```

## app (wiring)

- `app.APIDeps(cfg, log, pool, store, signer) (httpapi.Deps, error)` — every API service; `cmd/api` and the tests use it.
- `app.RegisterWorker(ctx, runner, cfg, log, pool, store) error` — ffmpeg check (non-fake STT), providers, all 5 job types, sweeps `upload_expiry`, `audio_retention`, `byte_purge`, `orphans`, `alerts`.
- `internal/app/app_test.go`: `TestCrossAccountAllRoutes` (production route table; a new route without a case fails) and `TestPipelineThroughProductionWiring`.

## meetings

Package `phoebe/internal/meetings` (meetings, folders, tags, deletion, pipeline state). Import it from uploads/transcripts/documents; it imports none of them.

```go
meetings.New(pool *pgxpool.Pool, cfg *config.Config, store storage.Storage, log *slog.Logger) *Service
// httpapi: MeetingsService = FoldersTagsService = *meetings.Service (wire the same instance into both Deps fields)

// pipeline_state (backend.md §3.1) — call inside your tx, after the meeting lock; bumps updated_at only when the state changed.
const StateAwaitingAudio, StateUploading, StateUploaded, StateTranscribing, StateTranscriptionFailed,
      StateGeneratingNotes, StateNotesFailed, StateNotesReady
type Event int
const UploadCreated        // awaiting_audio|uploading → uploading            (upload session create)
      UploadVerified       // uploading → uploaded                            (complete finalize)
      UploadAbandoned      // uploading → awaiting_audio iff no open/verifying session remains (set the session's status FIRST)
      TranscriptionStarted // uploaded|transcription_failed → transcribing    (start transcription tx)
      TranscriptWritten    // transcribing → generating_notes                 (transcribe success finalize)
      TranscriptionFailed  // transcribing → transcription_failed             (transcribe OnFailure)
      NotesRequested       // notes_ready|notes_failed → generating_notes     (user generate_notes)
      NotesWritten         // generating_notes → notes_ready                  (notes finalize)
      NotesFailed          // generating_notes → notes_failed                 (notes OnFailure)
func Transition(ctx, q db.Querier, meetingID string, ev Event) (changed bool, err error)   // not in a from-state → false, nothing written

// Shared API views (api.md §2/§4.2), reuse them in your responses:
type Meeting, ListItem, AudioAsset, UploadSession, Job, DocumentSummary, TranscriptRef, Documents, MeetingJobs, Label (Folder/Tag)
func JobView(j *jobs.Job) *Job                                                     // api.md Job object; nil → nil
func LatestJob(ctx, q, meetingID, jobType string) (*Job, error)                    // nil when none
func LoadAudio(ctx, q, meetingID string) (*AudioAsset, error)                      // nil when none; deleted → available=false
func LoadLiveUpload(ctx, q, meetingID string, chunkMaxBytes int64) (*UploadSession, error)  // open|verifying, nil when none
func LoadDocumentSummaries(ctx, q, meetingID string) (Documents, error)            // DocumentSummary incl. is_stale/stale_reasons (read-time)
func Load(ctx, q, cfg, userID, meetingID string) (*Meeting, error)                 // full meeting object; not owned/tombstoned → apperr.ErrNotFound

// Deletion
func TombstoneUser(ctx, tx pgx.Tx, userID string) error   // account-delete tx step: lock + tombstone all meetings (+deleted_meetings), cancel uploads + jobs
func CorrelationID(ctx) string                            // ctx correlation id or a fresh UUID (for system NewJob.CorrelationID)
(*Service).PurgeMeetingSpec() jobs.Spec                   // purge_meeting: Handler PurgeMeeting, Deadline 10 min, PurgeBackoff
// users: (*users.Service).Delete(ctx, uid) (purgeAt time.Time, err), (*users.Service).PurgeAccountSpec() jobs.Spec (30 min, PurgeBackoff)

// Service methods behind the routes (owner-scoped; service-level errors are apperr):
Create(ctx, uid, CreateInput) (*Meeting, created bool, error); Get(ctx, uid, mid); Update(ctx, uid, mid, Patch); List(ctx, uid, ListQuery) ([]ListItem, *Cursor, error)
Delete(ctx, uid, mid) (purgeAt time.Time, error); DeleteAudio(ctx, uid, mid) error
ListLabels / CreateLabel / RenameLabel / DeleteLabel(ctx, meetings.Folders|meetings.Tags, uid, …)
```
Notes for other domains:
- `documents.updated_at` is the summary's `updated_at`: bump it on every revision insert and review set/clear.
- `search_documents` rows (written by transcripts/documents) are what `GET /v1/meetings?q=` searches, plus title and tag names.
- `DocumentSummary` staleness: `IS DISTINCT FROM` on pinned transcript (vs latest), manual notes and (MoM) AI notes (vs current revision); all-null sources and `manual_notes` are never stale.
- `DELETE /v1/me` handler is `(*Server).deleteMe` in `httpapi/routes_account_delete.go`; it is live only once `routesUsers` lists `{Method: "DELETE", Path: "/v1/me", Handle: s.deleteMe}`.
