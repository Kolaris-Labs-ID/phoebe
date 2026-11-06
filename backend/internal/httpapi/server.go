package httpapi

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"phoebe/internal/config"
	"phoebe/internal/storage"
)

// SessionResolver resolves a bearer token to its session (implemented by the auth
// domain). Unknown/expired/revoked token or deleted account → an apperr Unauthenticated
// error; any other error is treated as a server failure.
type SessionResolver interface {
	ResolveSession(ctx context.Context, token string) (userID, sessionID string, err error)
}

// Deps carries every dependency handlers use. Domain service field types are declared
// in each domain's routes_<domain>.go, so domain agents change only their own file.
type Deps struct {
	Config   *config.Config
	Log      *slog.Logger
	DB       *pgxpool.Pool
	Storage  storage.Storage
	Signer   *storage.Signer
	Sessions SessionResolver

	Auth        AuthService
	Users       UsersService
	Meetings    MeetingsService
	FoldersTags FoldersTagsService
	Uploads     UploadsService
	Transcripts TranscriptsService
	Documents   DocumentsService
	Exports     ExportsService
	Files       FilesService
	Usage       UsageService
	Events      EventsService
}

// HandlerFunc returns an error instead of writing one; the server maps it to the
// api.md §1.1 envelope (apperr codes, see errors.go).
type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

// NoBodyLimit disables the 2 MiB JSON limit for routes that enforce their own (chunk PUT).
const NoBodyLimit int64 = -1

const DefaultBodyLimit int64 = 2 << 20

type Route struct {
	Method string
	Path   string // net/http pattern path, e.g. "/v1/meetings/{id}"
	Public bool   // true = no bearer auth
	// BodyLimit: 0 = DefaultBodyLimit, NoBodyLimit = none.
	BodyLimit int64
	Handle    HandlerFunc
}

// Pattern is the ServeMux pattern and the logged route, e.g. "GET /v1/meetings/{id}".
func (rt Route) Pattern() string { return rt.Method + " " + rt.Path }

type Server struct {
	Deps
	routes  []Route
	handler http.Handler
}

// New builds the router from the single route table (Routes) and wraps it in the
// global middleware: request id → access log → recover.
func New(d Deps) *Server {
	s := &Server{Deps: d}
	mux := http.NewServeMux()
	for _, f := range []func() []Route{
		s.routesConfigHealth,
		s.routesAuth,
		s.routesUsers,
		s.routesMeetings,
		s.routesFoldersTags,
		s.routesUploads,
		s.routesTranscripts,
		s.routesDocuments,
		s.routesExports,
		s.routesFiles,
		s.routesUsage,
		s.routesEvents,
	} {
		for _, rt := range f() {
			s.routes = append(s.routes, rt)
			mux.Handle(rt.Pattern(), s.wrapRoute(rt))
		}
	}
	mux.Handle("/", s.adapt(func(w http.ResponseWriter, r *http.Request) error {
		return errNotFound
	}))
	s.handler = s.requestID(s.accessLog(s.recoverer(mux)))
	return s
}

// Routes returns the route table (used by the cross-account test to cover every route).
func (s *Server) Routes() []Route { return append([]Route(nil), s.routes...) }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

func (s *Server) wrapRoute(rt Route) http.Handler {
	h := s.adapt(rt.Handle)
	if !rt.Public {
		h = s.authenticate(h)
	}
	limit := rt.BodyLimit
	if limit == 0 {
		limit = DefaultBodyLimit
	}
	if limit > 0 {
		h = bodyLimit(limit, h)
	}
	return h
}

func (s *Server) adapt(h HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			s.writeError(w, r, err)
		}
	})
}
