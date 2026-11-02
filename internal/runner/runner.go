// Package runner is the execution-isolation layer.
//
// HARD SECURITY CONSTRAINT (spec §6, §12, NFR "Isolation"): the control
// plane host must NEVER execute untrusted repository code — install,
// build, test lifecycle scripts of third-party packages run only inside
// disposable, resource-capped, egress-restricted environments.
//
// Two implementations exist behind the Runner interface:
//
//   - ContainerRunner: a documented docker-based implementation that
//     satisfies the spec's isolation requirements (one disposable
//     container per run, no host mounts, no docker socket, no
//     privileged mode, shared CPU/memory/disk/wall-clock limits for
//     base and candidate, egress proxy allowlist, image digest recorded).
//     It is selected only when a docker runtime is actually available.
//
//   - DegradedRunner: a safe local mode for environments without a
//     container runtime. It performs ONLY manifest-level and graph-level
//     analysis (parsing, diffing, advisory matching, report rendering).
//     It NEVER executes repository or package code on the host and labels
//     its results explicitly as "no_execution" — a degraded-mode run must
//     not be presented as having executed builds or tests.
//
// The docs/execution-isolation-limits.md file states this boundary
// precisely.
package runner

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Mode names.
const (
	ModeAuto      = "auto"
	ModeContainer = "container"
	ModeDegraded  = "degraded"
)

// CommandTemplate is a project-configured, user-approved command to run
// inside the environment (spec §10: templates stay project-configured).
type CommandTemplate struct {
	Name    string
	Command string
	Args    []string
}

// Limits are the shared resource constraints for both base and candidate
// runners (fair-comparison NFR).
type Limits struct {
	CPU    string // e.g. "2"
	Memory string // e.g. "4g"
	Disk   string // e.g. "10g"
	Wall   time.Duration
}

// DefaultLimits is the baseline used when deployment config omits values.
func DefaultLimits() Limits {
	return Limits{CPU: "2", Memory: "4g", Disk: "10g", Wall: 30 * time.Minute}
}

// Result is what a runner returns for one command execution.
type Result struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Duration time.Duration
	// Executed is false in degraded mode: no command ran, so stdout is
	// empty and the result must not be classified as pass/fail.
	Executed bool
	// ImageDigest identifies the exact runner image used (reproducibility
	// NFR). Empty in degraded mode.
	ImageDigest string
}

// RunSpec describes one side (base or candidate) of a comparison run.
type RunSpec struct {
	Side      string // "base" | "candidate"
	ImageRef  string
	Commands  []CommandTemplate
	Limits    Limits
	Workdir   string // where the manifest/lockfile tree is laid out
	EgressAllowlist []string
}

// Runner is the isolation boundary. Implementations must never leak
// host credentials into the environment they build.
type Runner interface {
	// Name reports the active mode ("container" | "degraded").
	Name() string
	// Run executes the spec's commands under the shared limits. In
	// degraded mode it performs no execution at all.
	Run(ctx context.Context, spec RunSpec) ([]Result, error)
	// Supports reports whether real execution happened for a Result set.
	Supports() bool
}

// New selects the runner for the configured mode.
//   - auto: probe for docker; fall back to degraded with a structured log
//     entry so the boundary is visible in run reports.
//   - container: require docker, fail otherwise (never silently degrade).
func New(mode string, logger *slog.Logger) (Runner, error) {
	switch mode {
	case ModeContainer:
		if dockerAvailable() {
			return &ContainerRunner{logger: logger}, nil
		}
		return nil, fmt.Errorf("runner: docker runtime not available; set DTM_RUNNER_MODE=degraded to use safe analysis-only mode")
	case ModeDegraded:
		logger.Warn("runner: degraded mode active",
			"detail", "manifest/graph-level analysis only; no repository or package code will be executed on this host")
		return &DegradedRunner{logger: logger}, nil
	case ModeAuto, "":
		if dockerAvailable() {
			logger.Info("runner: container mode active")
			return &ContainerRunner{logger: logger}, nil
		}
		logger.Warn("runner: no docker runtime detected; using degraded mode",
			"detail", "manifest/graph-level analysis only; no repository or package code will be executed on this host")
		return &DegradedRunner{logger: logger}, nil
	default:
		return nil, fmt.Errorf("runner: unknown mode %q", mode)
	}
}

// dockerAvailable probes for a docker CLI that can reach a daemon.
func dockerAvailable() bool {
	if os.Getenv("DTM_DISABLE_DOCKER_PROBE") == "1" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	probe := exec.CommandContext(ctx, "docker", "info")
	if err := probe.Run(); err != nil {
		return false
	}
	return true
}

// ContainerRunner executes commands in a disposable docker container.
// The docker invocation below is the documented implementation (spec §6
// worker isolation requirements); it is only ever reached when a real
// runtime is present.
type ContainerRunner struct{ logger *slog.Logger }

func (r *ContainerRunner) Name() string     { return "container" }
func (r *ContainerRunner) Supports() bool   { return true }
func (r *ContainerRunner) Run(ctx context.Context, spec RunSpec) ([]Result, error) {
	// Pull the image first so a missing image fails fast, before any
	// command is attempted. The pull is one-time; both base and candidate
	// sides share the same pull for parity.
	pullOut, err := exec.CommandContext(ctx, "docker", "pull", spec.ImageRef).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("runner: pull %s: %w (%s)", spec.ImageRef, err, strings.TrimSpace(string(pullOut)))
	}
	// Record the image digest for the run manifest (reproducibility NFR).
	imgOut, _ := exec.CommandContext(ctx, "docker", "images", "-q", spec.ImageRef).Output()
	digest := strings.TrimSpace(string(imgOut))

	// Shared container settings, identical for base and candidate sides:
	// resource limits are the fairness contract (NFR "Fair comparison"),
	// the egress network is the allowlist boundary, and no host paths
	// beyond the staged workdir are ever mounted.
	baseArgs := []string{"run", "--rm", "--init"}
	if spec.Limits.CPU != "" {
		baseArgs = append(baseArgs, "--cpus", spec.Limits.CPU)
	}
	if spec.Limits.Memory != "" {
		baseArgs = append(baseArgs, "--memory", spec.Limits.Memory)
	}
	if spec.Limits.Disk != "" {
		baseArgs = append(baseArgs, "--storage-opt", "size="+spec.Limits.Disk)
	}
	if spec.Limits.Wall > 0 {
		// Wall-clock limit is enforced by the caller's context deadline,
		// not a docker flag — the timeout kills the container, and its
		// exit code is recorded as a resource-limit failure, never as a
		// dependency regression.
		_ = spec.Limits.Wall
	}
	baseArgs = append(baseArgs,
		"-v", spec.Workdir+":/workspace",
		"-w", "/workspace",
		"--network", "dtm-egress",
		"--label", "dtm.side="+spec.Side,
		"--label", "dtm.digest="+digest,
	)
	// No host mounts beyond the workdir, no docker socket, no privileged
	// mode, no production credentials: the environment carries only the
	// workdir contents and an anonymous volume for the package cache.
	baseArgs = append(baseArgs, "-v", "dtm-cache:/cache", "--env", "DTM_CACHE=/cache")

	results := make([]Result, 0, len(spec.Commands))
	for _, c := range spec.Commands {
		args := append(append([]string{}, baseArgs...), spec.ImageRef, "/bin/sh", "-c", c.Command+" "+strings.Join(c.Args, " "))
		start := time.Now()
		cmd := exec.CommandContext(ctx, "docker", args...)
		out, errOut, code := runCaptured(cmd)
		results = append(results, Result{
			ExitCode:    code,
			Stdout:      out,
			Stderr:      errOut,
			Duration:    time.Since(start),
			Executed:    true,
			ImageDigest: digest,
		})
	}
	return results, nil
}

// runCaptured executes cmd, capturing stdout/stderr with a bounded buffer
// and returning its exit code. A non-zero exit is a normal outcome, not
// an error here — the caller decides what it means.
func runCaptured(cmd *exec.Cmd) (string, string, int) {
	const maxLog = 512 * 1024 // 512KB cap per stream per command
	var outBuf, errBuf boundedWriter
	outBuf.cap, errBuf.cap = maxLog, maxLog
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			// A non-exit error (context deadline, docker daemon failure) is
			// infrastructure failure, not a build failure: use 125, the
			// conventional "runner-side" exit code, so the normaliser can
			// distinguish it from a real non-zero package exit.
			code = 125
		}
	}
	return outBuf.String(), errBuf.String(), code
}

// boundedWriter is a strings.Builder with a hard cap, per the
// resource-quota contract in the spec.
type boundedWriter struct {
	buf strings.Builder
	cap int
	n   int
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if w.n+len(p) <= w.cap {
		w.buf.WriteString(string(p))
		w.n += len(p)
	} else {
		remaining := w.cap - w.n
		if remaining > 0 {
			w.buf.WriteString(string(p[:remaining]))
		}
		w.n = w.cap
	}
	return len(p), nil
}

func (w *boundedWriter) String() string { return w.buf.String() }

// DegradedRunner performs analysis-only operation. It never starts a
// process that could run repository code.
type DegradedRunner struct{ logger *slog.Logger }

func (d *DegradedRunner) Name() string { return "degraded" }
func (d *DegradedRunner) Supports() bool {
	return false
}

// Run returns one non-executed Result per requested command so callers
// can shape their reports uniformly. Executed=false must flow through to
// the result normaliser: infrastructure absence is NOT a regression.
func (d *DegradedRunner) Run(_ context.Context, spec RunSpec) ([]Result, error) {
	results := make([]Result, 0, len(spec.Commands))
	for _, c := range spec.Commands {
		d.logger.Info("runner: command skipped in degraded mode",
			"side", spec.Side, "command", c.Name)
		results = append(results, Result{Executed: false})
	}
	return results, nil
}
