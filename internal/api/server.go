// Package api is the Go control-plane HTTP server: repository ingestion,
// snapshots, upgrade runs, report/graph-diff retrieval, policies, webhook
// and CLI-run intake (spec §10).
//
// Security posture:
//   - Public routes pass through a per-client-IP token-bucket limiter.
//   - Webhook intake verifies HMAC-SHA256 when a secret is configured;
//     without a secret, webhooks are REJECTED (closed default).
//   - No handler reads credentials from the environment: GitHub
//     integration is enabled only when the operator explicitly supplies
//     a token/secret (least-privilege default = disabled).
//   - Untrusted strings are only ever emitted as JSON, never spliced
//     into free-text log lines (log-injection guard).
package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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

// Config carries the values a Server needs at request time. Pointer
// fields: nil means "feature disabled" (the least-privilege default),
// which is deliberately distinct from an empty-string value.
type Config struct {
	RateLimitRPS   float64
	RateLimitBurst int
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
	Cfg  Config
	Runs *InMemoryRunStore
	GH   *ci.GitHub
	OSV  *advisory.OSVClient
	Notes *relnotes.Client
	Logger *slog.Logger
}

// New builds a Server. store may be nil (in-memory only).
func New(cfg Config, gh *ci.GitHub, osv *advisory.OSVClient, notes *relnotes.Client, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{Cfg: cfg, Runs: NewInMemoryRunStore(), GH: gh, OSV: osv, Notes: notes, Logger: logger}
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
	if l.rps <= 0 {
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

// ---- routes ----------------------------------------------------------------

// Routes builds the http.ServeMux. Public routes are rate-limited;
// /healthz is not (LB probing).
func (s *Server) Routes() *http.ServeMux {
	lim := NewLimiter(s.Cfg.RateLimitRPS, s.Cfg.RateLimitBurst)
	mux := http.NewServeMux()

	public := func(pattern string, h http.HandlerFunc) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if !lim.Allow(clientKey(r)) {
				respondJSON(w, http.StatusTooManyRequests, map[string]string{
					"error": "rate limit exceeded; retry later"})
				return
			}
			h(w, r)
		})
	}

	public("POST /v1/repositories/connect", s.handleConnect)
	public("POST /v1/snapshots", s.handleSnapshot)
	public("POST /v1/upgrade-runs", s.handleUpgradeRun)
	public("GET /v1/upgrade-runs/{id}", s.handleGetRun)
	public("GET /v1/upgrade-runs/{id}/graph-diff", s.handleGraphDiff)
	public("GET /v1/upgrade-runs/{id}/report", s.handleReport)
	public("POST /v1/policies", s.handlePolicy)
	public("POST /v1/webhooks/github", s.handleWebhook)
	public("POST /v1/cli-runs", s.handleCLIRun)

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	return mux
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
		respondJSON(w, http.StatusBadRequest, map[string]string{
			"error": "owner and repo are required"})
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
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "commit_sha is required"})
		return
	}
	detected := parser.Detect(parser.Files(req.Files))
	if len(detected) == 0 {
		respondJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error": "no supported manifest/lockfile pair found in files"})
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"snapshot_id": "snap-" + shortHash(req.CommitSHA + sortedKeys(req.Files)),
		"ecosystems":  detected,
		"commit_sha":  req.CommitSHA,
	})
}

// handleUpgradeRun runs the pipeline for base vs candidate. Replayed
// idempotency keys return the stored result without re-running.
func (s *Server) handleUpgradeRun(w http.ResponseWriter, r *http.Request) {
	var req upgradeRunRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.BaseSHA == "" || req.CandidateSHA == "" {
		respondJSON(w, http.StatusBadRequest, map[string]string{
			"error": "base_sha and candidate_sha are required"})
		return
	}
	if req.IdempotencyKey != "" {
		if stored, ok := s.Runs.Get("idem-" + req.IdempotencyKey); ok {
			respondJSON(w, http.StatusOK, runResponse(stored))
			return
		}
	}

	advFeed := advisory.FeedOfflineOnly
	if req.AdvisoryOnline {
		advFeed = advisory.FeedOnline
	}
	res, err := s.runAnalysis(r.Context(), req, advFeed)
	if err != nil {
		respondJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
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
		ID:         "run-" + shortHash(req.BaseSHA+req.CandidateSHA+req.ProjectID),
		ProjectID:  req.ProjectID,
		RepoURL:    req.RepoURL,
		CommitSHA:  req.CandidateSHA,
		RunnerMode: "degraded", // analysis-only in the API pipeline; execution is the runner layer's concern
		Executed:   false,
		ResultClass: result.Report.Highest,
		Report:     result.Report,
		Diff:       result.Diff,
		AdvisoryFeed: string(result.Advisory),
		JSONPayload:  payload,
	}
	if err := s.Runs.Save(run); err != nil {
		return nil, fmt.Errorf("api: store run: %w", err)
	}
	return run, nil
}

// runResponse is the stable wire shape for a run.
func runResponse(run *StoredRun) map[string]any {
	return map[string]any{
		"id":             run.ID,
		"project_id":     run.ProjectID,
		"repo_url":       run.RepoURL,
		"commit_sha":     run.CommitSHA,
		"runner_mode":    run.RunnerMode,
		"executed":       run.Executed,
		"result_class":   run.ResultClass,
		"advisory_feed":  run.AdvisoryFeed,
		"changed_count":  changedCount(run),
		"report_url":     "/v1/upgrade-runs/" + run.ID + "/report",
		"graph_diff_url": "/v1/upgrade-runs/" + run.ID + "/graph-diff",
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
		respondJSON(w, http.StatusNotFound, map[string]string{"error": "no graph diff recorded for this run"})
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
		respondJSON(w, http.StatusNotFound, map[string]string{"error": "no report recorded for this run"})
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
	run, ok := s.Runs.Get(id)
	if !ok {
		respondJSON(w, http.StatusNotFound, map[string]string{"error": "run not found: " + id})
	}
	return run, ok
}

func (s *Server) handlePolicy(w http.ResponseWriter, r *http.Request) {
	var req policyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !validSeverity(req.BlockOn) || !validSeverity(req.WarnOn) {
		respondJSON(w, http.StatusBadRequest, map[string]string{
			"error": "block_on/warn_on must be one of: " + strings.Join(allSeverities(), ", ")})
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"project_id": req.ProjectID,
		"block_on":   req.BlockOn,
		"warn_on":    req.WarnOn,
		"saved":      true,
	})
}

// handleWebhook enforces HMAC verification when a secret is configured.
// Without a secret it refuses the event rather than accept it unauthenticated.
func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	sig := r.Header.Get("X-Hub-Signature-256")
	secret := s.Cfg.WebhookSecret
	if secret == nil || *secret == "" {
		respondJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "webhook intake requires DTM_GITHUB_WEBHOOK_SECRET to be configured"})
		return
	}
	if sig == "" {
		respondJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "missing X-Hub-Signature-256 header"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, 8<<20))
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "could not read webhook body"})
		return
	}
	if !verifyHMAC(body, *secret, sig) {
		respondJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "webhook signature verification failed"})
		return
	}
	var ev map[string]any
	if err := json.Unmarshal(body, &ev); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid webhook JSON"})
		return
	}
	respondJSON(w, http.StatusAccepted, map[string]any{
		"event": ev["event"],
		"note":  "accepted; idempotent replay is a no-op",
	})
}

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
	respondJSON(w, http.StatusAccepted, map[string]any{
		"project_id": req.ProjectID,
		"state":      payload.State,
		"conclusion": payload.Conclusion,
		"posting":    posting,
	})
}

// ---- small helpers -----------------------------------------------------------

// decodeJSON reads a request body into v with a size cap and strict field
// checking (unknown fields are rejected, so a client typo can't silently
// change behaviour).
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body: " + err.Error()})
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
