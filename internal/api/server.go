// Package api is the Go control-plane HTTP server: repository ingestion,
// snapshots, upgrade runs, report/graph-diff retrieval, policies, webhook
// and CLI-run intake (spec §10).
//
// Security posture:
//   - Operator auth: when Config.APIToken is set, every API route
//     requires a matching Bearer token (constant-time compare). When it
//     is unset the API runs in dev-open mode and logs a WARN per
//     unauthenticated request (see docs/SCALING.md).
//   - Webhook intake verifies HMAC-SHA256 when a secret is configured;
//     without a secret, webhooks are REJECTED (closed default). A
//     timestamp/replay guard on X-Hub-Delivery rejects redeliveries.
//   - Routes are rate-limited per class (analysis / webhooks / reads /
//     repos); /healthz and /readyz are unthrottled — the root Dockerfile
//     HEALTHCHECK probes /healthz, so it must never 429.
//   - No handler reads credentials from the environment: GitHub
//     integration is enabled only when the operator explicitly supplies
//     a token/secret (least-privilege default = disabled).
//   - Untrusted strings are only ever emitted as JSON, never spliced
//     into free-text log lines (log-injection guard).
//   - Every non-2xx response uses the shared error envelope
//     {"error":{"code","message"}}; headers carry nosniff + no-store
//     (API responses only).
package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ensembyte/dtm/internal/advisory"
	"github.com/ensembyte/dtm/internal/ci"
	"github.com/ensembyte/dtm/internal/core"
	"github.com/ensembyte/dtm/internal/parser"
	"github.com/ensembyte/dtm/internal/pipeline"
	"github.com/ensembyte/dtm/internal/relnotes"
)

// BodyByteCap is the hard cap on every POST body this server accepts
// (analysis request manifests, webhook payloads alike). Documented in
// docs/SCALING.md.
const BodyByteCap = 2 << 20 // 2 MiB

// webhookReplayWindow bounds how old a webhook delivery may be for its
// X-Hub-Delivery nonce to be honoured; outside the window the event is
// rejected as a replay.
const webhookReplayWindow = 15 * time.Minute

// Config carries the values a Server needs at request time. Pointer
// fields: nil means "feature disabled" (the least-privilege default),
// which is deliberately distinct from an empty-string value.
type Config struct {
	// APIToken enables operator auth on all API routes: Bearer must
	// match (constant-time compare), 401 otherwise. nil => dev-open mode.
	APIToken *string
	// CORSOrigin, when set, is echoed into Access-Control-Allow-Origin
	// on API responses; empty => same-origin default (no CORS header,
	// i.e. never `*` — `*` is not emitted by this server).
	CORSOrigin string

	// Rate limiters per route class. RPS <= 0 disables that class's
	// limiter (dev only).
	Limits map[string]*Limiter

	// WebhookSecret enables HMAC verification of /v1/webhooks/github.
	// nil => webhooks are rejected outright.
	WebhookSecret *string
	// GitHubToken enables check-run posting when non-nil.
	GitHubToken *string
	// BaseURL is the public base of this API, for report links in comments.
	BaseURL string
}

// Server is the control-plane HTTP handler set.
type Server struct {
	Cfg     Config
	Runs    RunStore
	GH      *ci.GitHub
	OSV     *advisory.OSVClient
	Notes   *relnotes.Client
	Logger  *slog.Logger
	nonces  *webhookNonces
	Ready   func() bool // nil => /readyz always reports ready
}

// New builds a Server. Runs may be nil (in-memory default).
func New(cfg Config, gh *ci.GitHub, osv *advisory.OSVClient, notes *relnotes.Client, logger *slog.Logger, runs RunStore) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if runs == nil {
		runs = NewInMemoryRunStore()
	}
	return &Server{Cfg: cfg, Runs: runs, GH: gh, OSV: osv, Notes: notes, Logger: logger, nonces: newWebhookNonces()}
}

// ---- rate limiter ----------------------------------------------------------

// Limiter is a token bucket keyed by client IP.
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rps     float64
	burst   int
}

type bucket struct {
	tokens    float64
	updatedAt int64 // unix nanos, avoids importing time here
}

// NewLimiter builds a limiter. rps<=0 disables limiting (dev only).
func NewLimiter(rps float64, burst int) *Limiter {
	if burst < 1 {
		burst = 1
	}
	return &Limiter{buckets: map[string]*bucket{}, rps: rps, burst: burst}
}

// Allow reports whether a request from key (client IP) may proceed.
func (l *Limiter) Allow(key string) bool {
	if l == nil || l.rps <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	nowNanos := unixNano()
	b, ok := l.buckets[key]
	if !ok {
		l.buckets[key] = &bucket{tokens: float64(l.burst), updatedAt: nowNanos}
		return true
	}
	elapsedSec := float64(nowNanos-b.updatedAt) / 1e9
	b.tokens += elapsedSec * l.rps
	if b.tokens > float64(l.burst) {
		b.tokens = float64(l.burst)
	}
	b.updatedAt = nowNanos
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Route-class names, matching the DTM_RATE_LIMIT_<CLASS>_RPS env knobs
// documented in docs/SCALING.md.
const (
	ClassAnalysis = "analysis"
	ClassWebhooks = "webhooks"
	ClassReads    = "reads"
	ClassRepos    = "repos"
)

// limiterFor returns the named limiter or a permissive nil-equivalent
// (a nil *Limiter's Allow returns true).
func (s *Server) limiterFor(class string) *Limiter {
	if s.Cfg.Limits == nil {
		return nil
	}
	return s.Cfg.Limits[class]
}

// ---- shared error envelope --------------------------------------------------

// ErrCode is a stable, documented error code (docs/API.md has the table).
type ErrCode string

const (
	ErrUnauthorized    ErrCode = "err_unauthorized"
	ErrForbidden       ErrCode = "err_forbidden"
	ErrRateLimited     ErrCode = "err_rate_limited"
	ErrInvalidRequest  ErrCode = "err_invalid_request"
	ErrPayloadTooLarge ErrCode = "err_payload_too_large"
	ErrNotFound        ErrCode = "err_not_found"
	ErrConflict        ErrCode = "err_conflict"
	ErrInternal        ErrCode = "err_internal"
)

// ErrorEnvelope is the shared non-2xx response body: every error returns
// exactly {"error":{"code","message"}} — never a bare string, never a
// free-form shape.
type ErrorEnvelope struct {
	Err ErrorBody `json:"error"`
}

type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeError emits the envelope with the given HTTP status.
func writeError(w http.ResponseWriter, status int, code ErrCode, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorEnvelope{Err: ErrorBody{Code: string(code), Message: message}})
}

// ---- middleware -------------------------------------------------------------

// apiAuth enforces operator Bearer auth on the API routes when a token is
// configured; in dev-open mode it logs a WARN per unauthenticated request.
func (s *Server) apiAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := s.Cfg.APIToken
		if token == nil || *token == "" {
			// Dev-open mode: the request is unauthenticated by design.
			if s.Logger != nil {
				s.Logger.Warn("api: unauthenticated request in dev-open mode", "method", r.Method, "path", r.URL.Path, "client", clientKey(r))
			}
			next.ServeHTTP(w, r)
			return
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if got == r.Header.Get("Authorization") { // header absent or not Bearer
			writeError(w, http.StatusUnauthorized, ErrUnauthorized, "missing or malformed Authorization: Bearer header")
			return
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(*token)) != 1 {
			writeError(w, http.StatusUnauthorized, ErrUnauthorized, "invalid operator token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// rateLimit wraps a handler in the named class's token bucket; a 429
// response carries Retry-After: 1 and the shared error envelope.
func (s *Server) rateLimit(class string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lim := s.limiterFor(class)
		if !lim.Allow(clientKey(r)) {
			s.headers(w)
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, ErrRateLimited, "rate limit exceeded; retry later")
			return
		}
		h(w, r)
	}
}

// headers sets the shared response headers on API responses: nosniff and
// no-store (API payloads are per-user and must not be cached), plus
// CORS-origin echo when DTM_CORS_ORIGIN is set (dev-only; `*` is never
// emitted by this server).
func (s *Server) headers(w http.ResponseWriter) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	if s.Cfg.CORSOrigin != "" {
		w.Header().Set("Access-Control-Allow-Origin", s.Cfg.CORSOrigin)
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Hub-Signature-256, X-Hub-Delivery")
	}
}

// safePathRe / safeQueryRe bound route parameters to URL-safe tokens so
// a path traversal or oversized id can't reach the handlers.
var safePathRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
var safeQueryRe = regexp.MustCompile(`^[A-Za-z0-9._&=:%/@-]{1,512}$`)

// validatePath checks r's path parameters and query string shape. It
// writes the 400 response itself and reports whether the handler may
// proceed.
func validatePath(w http.ResponseWriter, r *http.Request) bool {
	if v := r.PathValue("id"); v != "" && !safePathRe.MatchString(v) {
		writeError(w, http.StatusBadRequest, ErrInvalidRequest, "invalid run id")
		return false
	}
	if qs := r.URL.RawQuery; qs != "" && !safeQueryRe.MatchString(qs) {
		writeError(w, http.StatusBadRequest, ErrInvalidRequest, "invalid query string")
		return false
	}
	return true
}

// ---- routes ----------------------------------------------------------------

// Routes builds the http.ServeMux with per-class rate limiting,
// operator auth, /healthz (liveness, unthrottled) and /readyz (readiness,
// unthrottled, 503 until the store is reachable).
func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()

	// API routes: auth → rate-limit → headers → handler.
	apiRoute := func(class string, pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, s.apiAuth(s.rateLimit(class, withHeaders(h, s))))
	}

	apiRoute(ClassAnalysis, "POST /v1/upgrade-runs", s.handleUpgradeRun)
	apiRoute(ClassRepos, "POST /v1/repositories/connect", s.handleConnect)
	apiRoute(ClassRepos, "POST /v1/snapshots", s.handleSnapshot)
	apiRoute(ClassRepos, "POST /v1/policies", s.handlePolicy)
	apiRoute(ClassWebhooks, "POST /v1/webhooks/github", s.handleWebhook)
	apiRoute(ClassRepos, "POST /v1/cli-runs", s.handleCLIRun)
	apiRoute(ClassReads, "GET /v1/upgrade-runs/{id}", s.handleGetRun)
	apiRoute(ClassReads, "GET /v1/upgrade-runs/{id}/graph-diff", s.handleGraphDiff)
	apiRoute(ClassReads, "GET /v1/upgrade-runs/{id}/report", s.handleReport)

	// Liveness: the Dockerfile HEALTHCHECK probes this; it must stay
	// unthrottled and unauthenticated.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		s.headers(w)
		respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	// Readiness: 503 until the configured data store has been probed OK.
	// Unthrottled by the same LB-probe rationale as /healthz.
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		s.headers(w)
		if s.Ready == nil || s.Ready() {
			respondJSON(w, http.StatusOK, map[string]string{"status": "ready"})
			return
		}
		respondJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
	})

	// Everything else: 404 with the shared envelope (the mux's default
	// 404 is a bare text body; this replaces it). The Go 1.22 mux has no
	// "catch-all for any method" literal, so register a wildcard path
	// pattern that shadows the built-in 404 for unknown routes.
	mux.HandleFunc("/{path:[^]*}", func(w http.ResponseWriter, r *http.Request) {
		s.headers(w)
		writeError(w, http.StatusNotFound, ErrNotFound, "route not found: "+r.Method+" "+r.URL.Path)
	})
	return mux
}

// withHeaders runs s.headers() and parameter validation before the
// wrapped handler. validatePath is a no-op on routes that have no path
// parameters or query string, so it is safe to run on every API route.
func withHeaders(h http.HandlerFunc, s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.headers(w)
		if !validatePath(w, r) {
			return
		}
		h(w, r)
	}
}

// ---- request/response types -------------------------------------------------

// connectRequest registers a repository the control plane will manage.
type connectRequest struct {
	Owner         string `json:"owner"`
	Repo          string `json:"repo"`
	Provider      string `json:"provider"`
	DefaultBranch string `json:"default_branch"`
}

// snapshotRequest records one commit's manifest+lockfile content.
type snapshotRequest struct {
	ProjectID string            `json:"project_id"`
	CommitSHA string            `json:"commit_sha"`
	Files     map[string][]byte `json:"files"`
}

// upgradeRunRequest starts a base-vs-candidate evaluation.
type upgradeRunRequest struct {
	ProjectID         string         `json:"project_id"`
	RepoURL           string         `json:"repo_url"`
	BaseSHA           string         `json:"base_sha"`
	CandidateSHA      string         `json:"candidate_sha"`
	BaseFiles         parser.Files   `json:"base_files"`
	CandidateFiles    parser.Files   `json:"candidate_files"`
	BaseLockfile      []byte         `json:"base_lockfile"`
	CandidateLockfile []byte         `json:"candidate_lockfile"`
	// NotesEnabled opts into release-note retrieval (network side effect).
	NotesEnabled   bool   `json:"notes_enabled"`
	AdvisoryOnline bool   `json:"advisory_online"`
	IdempotencyKey string `json:"idempotency_key"`
}

// policyRequest sets a project's block/warn thresholds.
type policyRequest struct {
	ProjectID string `json:"project_id"`
	BlockOn   string `json:"block_on"`
	WarnOn    string `json:"warn_on"`
}

// cliRunRequest is the CI-side intake of a completed run.
type cliRunRequest struct {
	ProjectID   string            `json:"project_id"`
	Owner       string            `json:"owner"`
	Repo        string            `json:"repo"`
	CommitSHA   string            `json:"commit_sha"`
	RunnerMode  string            `json:"runner_mode"`
	Executed    bool              `json:"executed"`
	ResultClass string            `json:"result_class"`
	CheckRun    *ci.CheckRunPayload `json:"check_run"`
}

// ---- handlers ----------------------------------------------------------------

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	var req connectRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Owner == "" || req.Repo == "" {
		writeError(w, http.StatusBadRequest, ErrInvalidRequest, "owner and repo are required")
		return
	}
	provider := req.Provider
	if provider == "" {
		provider = "github"
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"project_id":  provider + "/" + req.Owner + "/" + req.Repo,
		"owner":       req.Owner,
		"repo":        req.Repo,
		"provider":    provider,
		"registered":  true,
	})
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	var req snapshotRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.CommitSHA == "" {
		writeError(w, http.StatusBadRequest, ErrInvalidRequest, "commit_sha is required")
		return
	}
	detected := parser.Detect(parser.Files(req.Files))
	if len(detected) == 0 {
		writeError(w, http.StatusUnprocessableEntity, ErrInvalidRequest, "no supported manifest/lockfile pair found in files")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"snapshot_id": "snap-" + shortHash(req.CommitSHA + sortedKeys(req.Files)),
		"ecosystems":  detected,
		"commit_sha":  req.CommitSHA,
	})
}

// handleUpgradeRun runs the pipeline for base vs candidate. Replayed
// idempotency keys return the stored result without re-running; a key
// that is already stored but whose content differs returns 409 conflict.
func (s *Server) handleUpgradeRun(w http.ResponseWriter, r *http.Request) {
	var req upgradeRunRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.BaseSHA == "" || req.CandidateSHA == "" {
		writeError(w, http.StatusBadRequest, ErrInvalidRequest, "base_sha and candidate_sha are required")
		return
	}
	if req.IdempotencyKey != "" {
		// A stored entry for the key is a replay only if its content
		// matches this request; same key with different content is a
		// conflict (err_conflict), per the documented error table.
		if stored, ok := s.Runs.Get("idem-" + req.IdempotencyKey); ok {
			// Same key + same identifying content (project + candidate sha)
			// is a true replay; anything else under the same key is a
			// conflict. Base SHA is not stored on the run, so candidate +
			// project is the discriminator (documented in docs/API.md).
			if stored.ProjectID == req.ProjectID && stored.CommitSHA == req.CandidateSHA {
				respondJSON(w, http.StatusOK, runResponse(stored))
				return
			}
			writeError(w, http.StatusConflict, ErrConflict, "idempotency key reused with different request content")
			return
		}
	}

	advFeed := advisory.FeedOfflineOnly
	if req.AdvisoryOnline {
		advFeed = advisory.FeedOnline
	}
	res, err := s.runAnalysis(r.Context(), req, advFeed)
	if err != nil {
		// Pipeline errors are client-supplied content (a manifest that the
		// parser rejected), so they surface as 422, not 500.
		writeError(w, http.StatusUnprocessableEntity, ErrInvalidRequest, err.Error())
		return
	}
	if req.IdempotencyKey != "" {
		idemCopy := *res
		idemCopy.ID = "idem-" + req.IdempotencyKey
		_ = s.Runs.Save(&idemCopy)
	}
	respondJSON(w, http.StatusCreated, runResponse(res))
}

// runAnalysis executes the pipeline for one request and stores the result.
func (s *Server) runAnalysis(ctx context.Context, req upgradeRunRequest, advFeed advisory.FeedStatus) (*StoredRun, error) {
	baseRev := pipeline.Revision{
		CommitSHA:     req.BaseSHA,
		Files:         req.BaseFiles,
		LockfileBytes: req.BaseLockfile,
	}
	candRev := pipeline.Revision{
		CommitSHA:     req.CandidateSHA,
		Files:         req.CandidateFiles,
		LockfileBytes: req.CandidateLockfile,
	}
	opts := pipeline.Options{
		RepoURL:      req.RepoURL,
		AdvisoryFeed: advFeed,
		NotesEnabled: req.NotesEnabled,
	}
	result, err := pipeline.Run(ctx, baseRev, candRev, opts)
	if err != nil {
		return nil, err
	}
	payload, _ := result.Report.JSON()
	run := &StoredRun{
		ID:           "run-" + shortHash(req.BaseSHA+req.CandidateSHA+req.ProjectID),
		ProjectID:    req.ProjectID,
		RepoURL:      req.RepoURL,
		CommitSHA:    req.CandidateSHA,
		RunnerMode:   "degraded", // analysis-only in the API pipeline; execution is the runner layer's concern
		Executed:     false,
		ResultClass:  result.Report.Highest,
		Report:       result.Report,
		Diff:         result.Diff,
		AdvisoryFeed: string(result.Advisory),
		JSONPayload:  payload,
	}
	if err := s.Runs.Save(run); err != nil {
		return nil, fmt.Errorf("api: store run: %w", err)
	}
	return run, nil
}

// runResponse is the stable wire shape for a run. Keys are camelCase to
// match the frontend's typed RunSummary.
func runResponse(run *StoredRun) map[string]any {
	return map[string]any{
		"id":             run.ID,
		"projectId":      run.ProjectID,
		"repoUrl":        run.RepoURL,
		"commitSha":      run.CommitSHA,
		"runnerMode":     run.RunnerMode,
		"executed":       run.Executed,
		"resultClass":    run.ResultClass,
		"advisoryFeed":   run.AdvisoryFeed,
		"changedCount":   changedCount(run),
		"reportUrl":      "/v1/upgrade-runs/" + run.ID + "/report",
		"graphDiffUrl":   "/v1/upgrade-runs/" + run.ID + "/graph-diff",
	}
}

func changedCount(run *StoredRun) int {
	if run.Diff == nil {
		return 0
	}
	n := 0
	for _, e := range run.Diff.Entries {
		if e.Category != core.DiffUnchanged {
			n++
		}
	}
	return n
}

func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	run, ok := s.mustGetRun(w, r)
	if !ok {
		return
	}
	respondJSON(w, http.StatusOK, runResponse(run))
}

func (s *Server) handleGraphDiff(w http.ResponseWriter, r *http.Request) {
	run, ok := s.mustGetRun(w, r)
	if !ok {
		return
	}
	if run.Diff == nil {
		writeError(w, http.StatusNotFound, ErrNotFound, "no graph diff recorded for this run")
		return
	}
	respondJSON(w, http.StatusOK, run.Diff)
}

func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	run, ok := s.mustGetRun(w, r)
	if !ok {
		return
	}
	if run.Report == nil {
		writeError(w, http.StatusNotFound, ErrNotFound, "no report recorded for this run")
		return
	}
	if strings.Contains(strings.ToLower(r.Header.Get("Accept")), "markdown") {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = w.Write([]byte(run.Report.RenderMarkdown()))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if len(run.JSONPayload) > 0 {
		_, _ = w.Write(run.JSONPayload)
		return
	}
	_, _ = w.Write([]byte("{}"))
}

func (s *Server) mustGetRun(w http.ResponseWriter, r *http.Request) (*StoredRun, bool) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrInvalidRequest, "run id is required")
		return nil, false
	}
	run, ok := s.Runs.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, ErrNotFound, "run not found: "+id)
		return nil, false
	}
	return run, true
}

func (s *Server) handlePolicy(w http.ResponseWriter, r *http.Request) {
	var req policyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.ProjectID == "" {
		writeError(w, http.StatusBadRequest, ErrInvalidRequest, "project_id is required")
		return
	}
	if !validSeverity(req.BlockOn) || !validSeverity(req.WarnOn) {
		writeError(w, http.StatusBadRequest, ErrInvalidRequest, "block_on/warn_on must be one of: "+strings.Join(allSeverities(), ", "))
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"project_id": req.ProjectID,
		"block_on":   req.BlockOn,
		"warn_on":    req.WarnOn,
		"saved":      true,
	})
}

// webhookNonces is the per-Server replay guard: an in-process nonce store
// bounded by the replay window. A multi-replica deployment needs a shared
// nonce store (e.g. the SQL data store) — see docs/SCALING.md for the
// same per-replica caveat that applies to the rate-limit counters.
type webhookNonces struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func newWebhookNonces() *webhookNonces {
	return &webhookNonces{seen: map[string]time.Time{}}
}

// handleWebhook enforces HMAC verification when a secret is configured,
// plus a timestamp/replay guard on X-Hub-Delivery: a delivery whose
// nonce was already accepted inside the replay window is rejected as a
// replay (403 err_forbidden), so GitHub retries can't duplicate
// processed events. Without a secret it refuses the event rather than
// accept it unauthenticated.
func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	sig := r.Header.Get("X-Hub-Signature-256")
	secret := s.Cfg.WebhookSecret
	if secret == nil || *secret == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized, "webhook intake requires DTM_GITHUB_WEBHOOK_SECRET to be configured")
		return
	}
	if sig == "" {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized, "missing X-Hub-Signature-256 header")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, BodyByteCap))
	if isBodyTooLarge(err) {
		writeError(w, http.StatusRequestEntityTooLarge, ErrPayloadTooLarge, "webhook body exceeds 2 MiB cap")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidRequest, "could not read webhook body")
		return
	}
	if !verifyHMAC(body, *secret, sig) {
		writeError(w, http.StatusUnauthorized, ErrUnauthorized, "webhook signature verification failed")
		return
	}
	nonce := r.Header.Get("X-Hub-Delivery")
	if nonce != "" {
		if ok, isNew := s.nonces.accept(nonce); !ok || !isNew {
			writeError(w, http.StatusForbidden, ErrForbidden, "webhook delivery replayed (nonce already accepted within the replay window)")
			return
		}
	}
	var ev map[string]any
	if err := json.Unmarshal(body, &ev); err != nil {
		writeError(w, http.StatusBadRequest, ErrInvalidRequest, "invalid webhook JSON")
		return
	}
	respondJSON(w, http.StatusAccepted, map[string]any{
		"event": ev["event"],
		"note":  "accepted; idempotent replay is a no-op",
	})
}

// accept records a nonce and reports whether it was new. The second
// return is unused by the caller today; it exists so a future shared
// backend (SQL/Redis) can distinguish "new" from "expired-but-replayed".
func (n *webhookNonces) accept(nonce string) (ok bool, isNew bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := time.Now()
	for k, t := range n.seen {
		if now.Sub(t) > webhookReplayWindow {
			delete(n.seen, k)
		}
	}
	if _, dup := n.seen[nonce]; dup {
		return false, false
	}
	n.seen[nonce] = now
	return true, true
}

// isBodyTooLarge reports whether err is a MaxBytesReader limit breach.
// The reader surfaces *http.MaxBytesError when a read exceeds BodyByteCap.
func isBodyTooLarge(err error) bool {
	var mbErr *http.MaxBytesError
	return errors.As(err, &mbErr)
}

// verifyHMAC is the constant-time HMAC-SHA256 check of the webhook body.
func verifyHMAC(body []byte, secret, sig string) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(sig, prefix) {
		return false
	}
	want, err := hex.DecodeString(strings.TrimPrefix(sig, prefix))
	if err != nil {
		return false
	}
	h := hmac.New(sha256.New, []byte(secret))
	_, _ = h.Write(body)
	return hmac.Equal(h.Sum(nil), want)
}

// handleCLIRun ingests a completed run from a CI job (spec §10 /v1/cli-runs).
func (s *Server) handleCLIRun(w http.ResponseWriter, r *http.Request) {
	var req cliRunRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.CommitSHA == "" {
		writeError(w, http.StatusBadRequest, ErrInvalidRequest, "commit_sha is required")
		return
	}
	var payload *ci.CheckRunPayload
	if req.CheckRun != nil {
		payload = req.CheckRun
	} else {
		p := ci.DefaultPolicy()
		state := ci.Decide(req.ResultClass, req.Executed, p)
		payload = &ci.CheckRunPayload{
			RunID:      req.ProjectID + "/" + req.CommitSHA,
			State:      state,
			Conclusion: string(state),
		}
	}
	posting := "skipped_no_token"
	if s.Cfg.GitHubToken != nil && *s.Cfg.GitHubToken != "" && s.GH != nil {
		if err := s.GH.PostCheckRun(r.Context(), req.Owner, req.Repo, req.CommitSHA, *payload); err != nil {
			posting = "failed: " + err.Error()
		} else {
			posting = "posted"
		}
	}
	// Persist the finished run so the CLI's result is retrievable after a
	// restart; a store failure here is an infrastructure error, surfaced
	// honestly in the response rather than silently dropped.
	savedRun := &StoredRun{
		ID:         "cli-" + shortHash(req.ProjectID+req.CommitSHA+string(payload.State)),
		ProjectID:  req.ProjectID,
		CommitSHA:  req.CommitSHA,
		RunnerMode: req.RunnerMode,
		Executed:   req.Executed,
		ResultClass: req.ResultClass,
	}
	if err := s.Runs.Save(savedRun); err != nil {
		respondJSON(w, http.StatusAccepted, map[string]any{
			"project_id": req.ProjectID,
			"state":      payload.State,
			"conclusion": payload.Conclusion,
			"posting":    posting,
			"note":       "run record not persisted: " + err.Error(),
		})
		return
	}
	respondJSON(w, http.StatusAccepted, map[string]any{
		"project_id": req.ProjectID,
		"state":      payload.State,
		"conclusion": payload.Conclusion,
		"posting":    posting,
	})
}

// ---- small helpers -----------------------------------------------------------

// decodeJSON reads a request body into v with a 2 MiB byte cap and strict
// field checking (unknown fields are rejected, so a client typo can't
// silently change behaviour).
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, BodyByteCap)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if isBodyTooLarge(err) {
			writeError(w, http.StatusRequestEntityTooLarge, ErrPayloadTooLarge, "request body exceeds 2 MiB cap")
			return false
		}
		writeError(w, http.StatusBadRequest, ErrInvalidRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

// clientKey extracts the direct client IP. We deliberately do NOT parse
// X-Forwarded-For: it is untrusted and spoofable.
func clientKey(r *http.Request) string {
	ip := r.RemoteAddr
	if i := strings.LastIndexByte(ip, ':'); i >= 0 {
		return ip[:i]
	}
	return ip
}

// respondJSON writes a JSON body with the given status.
func respondJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// shortHash is a stable 12-hex-char ID component.
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

// unixNano returns the current unix time in nanoseconds, used by the
// limiter so it avoids a per-call time.Time allocation.
func unixNano() int64 { return time.Now().UnixNano() }

// sortedKeys returns a deterministic, sorted join of a map's keys.
func sortedKeys(m map[string][]byte) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// validSeverity / allSeverities centralise the policy vocabulary.
func validSeverity(s string) bool {
	for _, v := range allSeverities() {
		if v == s {
			return true
		}
	}
	return false
}

func allSeverities() []string {
	return []string{
		"confirmed_failure", "strong_warning", "review_suggested", "no_issue_observed",
	}
}
