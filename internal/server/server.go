// Package server is the HTTP layer: a faithful port of the Flask routes in the
// Python server/app.py. It wires stdlib net/http (Go 1.22 method+path ServeMux
// patterns) to the db, firewall, auth, and config packages, mirroring every
// route and response shape so the UNCHANGED single-file web client (internal/
// static/index.html) works against it without modification.
//
// Endpoint behavior (auth order, status codes, error strings, the knock
// reconcile-then-record flow, the TOTP step-up gate, the H-9 client-IP model) is
// kept 1:1 with app.py; see the per-handler comments in knock.go / admin.go /
// totp.go.
package server

import (
	"net/http"

	"nft-okboy/internal/config"
	"nft-okboy/internal/db"
	"nft-okboy/internal/firewall"
)

// Server holds the shared dependencies every handler needs. It is the Go
// analogue of the closure state Flask's create_app() captured (db, ufw, cfg,
// ttl, throttle/anomaly thresholds — all derived from cfg here).
type Server struct {
	db      *db.DB
	fw      *firewall.Manager
	cfg     *config.Config
	version string
}

// NewServer constructs a Server from its dependencies. Version defaults to "dev"
// until SetVersion is called by main (which reads the VERSION file / build flag),
// mirroring how app.py reads VERSION as the single source of truth.
func NewServer(d *db.DB, fw *firewall.Manager, cfg *config.Config) *Server {
	return &Server{db: d, fw: fw, cfg: cfg, version: "dev"}
}

// SetVersion sets the version string reported by /health (and the X-... surfaces
// that might use it). main injects the VERSION-file value here.
func (s *Server) SetVersion(v string) {
	if v != "" {
		s.version = v
	}
}

// Routes builds the full route table and wraps it in the per-IP throttle gate
// (the equivalent of Flask's @before_request _throttle_gate). Go 1.22 method+path
// patterns give us the same routing app.py expressed with @app.route(methods=...);
// path params are read in-handler via r.PathValue(...).
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// ---- Client API ---- //
	mux.HandleFunc("POST /api/knock", s.knock)
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("GET /api/me/groups", s.myGroups)
	mux.HandleFunc("PATCH /api/me/membership/{group_id}", s.selfToggleMembership)
	mux.HandleFunc("PATCH /api/membership/{user_id}/{group_id}", s.toggleMembership)

	// ---- Admin: users ---- //
	mux.HandleFunc("GET /api/admin/users", s.adminListUsers)
	mux.HandleFunc("POST /api/admin/users", s.adminCreateUser)
	mux.HandleFunc("DELETE /api/admin/users/{user_id}", s.adminDeleteUser)
	mux.HandleFunc("POST /api/admin/users/{user_id}/admin", s.adminSetAdmin)
	mux.HandleFunc("GET /api/admin/users/{user_id}/groups", s.adminUserGroups)
	mux.HandleFunc("POST /api/admin/users/{user_id}/groups", s.adminAddMembership)
	mux.HandleFunc("POST /api/admin/users/{user_id}/revoke", s.adminRevokeUser)

	// ---- Admin: memberships ---- //
	mux.HandleFunc("POST /api/admin/memberships/remove", s.adminRemoveMembership)

	// ---- Admin: groups ---- //
	mux.HandleFunc("GET /api/admin/groups", s.adminListGroups)
	mux.HandleFunc("POST /api/admin/groups", s.adminCreateGroup)
	mux.HandleFunc("DELETE /api/admin/groups/{group_id}", s.adminDeleteGroup)

	// ---- Admin: audit ---- //
	mux.HandleFunc("GET /api/admin/audit", s.adminListAudit)

	// ---- Admin: TOTP ---- //
	mux.HandleFunc("POST /api/admin/totp/enroll", s.totpEnroll)
	mux.HandleFunc("POST /api/admin/totp/activate", s.totpActivate)
	mux.HandleFunc("DELETE /api/admin/totp", s.totpDisable)

	// ---- API JSON 404 fallback ---- //
	// Any /api/ request the patterns above do not match — an unknown path, or a
	// known path with the wrong method (Go 1.22 routes both to this less-specific
	// subtree pattern) — gets the {ok:false,error} JSON envelope instead of the
	// mux's PLAIN-TEXT 404/405, which the JSON-parsing SPA / API clients cannot
	// read. This mirrors the backend half of the upstream JSON error contract.
	mux.HandleFunc("/api/", s.apiNotFound)

	// ---- Health (no auth) ---- //
	mux.HandleFunc("GET /health", s.health)

	// ---- Web client (embedded SPA) ---- //
	// "/" serves index.html; "/static/..." also serves the same single-file SPA
	// so the app keeps working if the client requests its old static path. The
	// throttle gate only guards "/api/", so these pass through untouched.
	mux.HandleFunc("GET /", s.serveIndex)
	mux.HandleFunc("GET /static/", s.serveIndex)

	return s.throttleGate(mux)
}

// apiNotFound is the JSON catch-all for any /api/ request the route table does
// not match (unknown path, or a known path with the wrong method). It keeps the
// API's {ok:false,"error"} contract instead of the mux's plain-text 404/405 — the
// Go counterpart of app.py's @errorhandler JSON responses.
func (s *Server) apiNotFound(w http.ResponseWriter, r *http.Request) {
	errJSON(w, http.StatusNotFound, "Not found")
}

// health is the no-auth liveness probe. Shape mirrors app.py's /health plus the
// version field the Go build surfaces: {"ok":true,"service":"okboy","version":...}.
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"service": "okboy",
		"version": s.version,
	})
}
