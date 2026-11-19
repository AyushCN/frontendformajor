package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/api-sandbox-links/backend/internal/db"
	"github.com/api-sandbox-links/backend/internal/runtime"
	dockerclient "github.com/docker/docker/client"
)

// DevContainerLabel marks a container as a development-preview container.
// It is distinct from SandboxIDLabel so FindContainersBySandbox finds both
// production and dev containers when cleaning up a sandbox.
const DevContainerLabel = "api-sandbox-links.dev-container"

// DevContainerOptions describes the parameters needed to start a development
// container for a sandbox.
type DevContainerOptions struct {
	SandboxID    string
	Subdomain    string
	WorkspaceDir string // absolute host-side path to bind-mount (e.g. /tmp/.../work/myapp)
	Language     string
	Framework    string
	Port         int
	Env          []string // KEY=VALUE pairs, already decrypted
}

// devWorkspaceTarget is the in-container path where the workspace is mounted.
// It is a constant so tests can assert against it without hardcoding strings.
const devWorkspaceTarget = "/workspace"

// StartDevContainer selects the appropriate DevStrategy, starts a base-image
// development container with the workspace bind-mounted, and returns the
// container details. The caller is responsible for persisting DevContainerID
// and DevContainerPort to the sandbox row in the database.
//
// Security guarantees:
//   - Only WorkspaceDir is bind-mounted — never the Docker socket or the host FS.
//   - The container retains all sandbox isolation controls: CapDrop ALL,
//     NET_BIND_SERVICE, no-new-privileges, PID/memory/CPU limits.
//   - The base image is a plain upstream runtime image, not a Railpack/Nixpacks
//     build that could carry arbitrary build artifacts.
//   - The dev container is placed on the sandbox's existing private bridge
//     network, so it cannot reach other tenants' containers.
func (o *Orchestrator) StartDevContainer(ctx context.Context, opts DevContainerOptions) (*RunResult, error) {
	if opts.WorkspaceDir == "" {
		return nil, fmt.Errorf("orchestrator: dev container requires a non-empty workspace directory")
	}
	// Reject un-cleaned paths to prevent path-confusion attacks.
	cleaned := filepath.Clean(opts.WorkspaceDir)
	if cleaned != opts.WorkspaceDir {
		return nil, fmt.Errorf("orchestrator: workspace path is not clean: %q → %q", opts.WorkspaceDir, cleaned)
	}

	strategy := SelectDevStrategy(opts.Language, opts.Framework, nil)
	if strategy == nil {
		return nil, fmt.Errorf(
			"orchestrator: no dev strategy found for language=%q framework=%q — use deployment mode instead",
			opts.Language, opts.Framework)
	}

	port := opts.Port
	if port == 0 {
		port = 8080
	}

	command := strategy.DevCommand(devWorkspaceTarget, port, opts.Env)

	labels := map[string]string{
		SandboxIDLabel:                    opts.SandboxID,
		DevContainerLabel:                 "true",
		"api-sandbox-links.subdomain":     opts.Subdomain,
		"api-sandbox-links.dev-strategy":  strategy.Name(),
	}

	mounts := []runtime.Mount{
		{
			Type:     "bind",
			Source:   opts.WorkspaceDir,
			Target:   devWorkspaceTarget,
			ReadOnly: false,
		},
	}

	// Use "<subdomain>-dev" as the container's subdomain label so that if the
	// gateway ever needs to route dev traffic by label, it has a distinct key.
	runOpts := RunOptions{
		SandboxID:    opts.SandboxID,
		ImageTag:     strategy.BaseImage(),
		Subdomain:    opts.Subdomain + "-dev",
		InternalPort: port,
		Env:          opts.Env,
		NetworkName:  netName(opts.SandboxID),
		Labels:       labels,
		Command:      command,
		Mounts:       mounts,
	}

	result, err := o.RunSandbox(ctx, runOpts)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: starting dev container for sandbox %s (strategy=%s): %w",
			opts.SandboxID, strategy.Name(), err)
	}

	o.logger.Info("dev container started",
		slog.String("sandbox", opts.SandboxID),
		slog.String("container", result.ContainerID),
		slog.String("strategy", strategy.Name()),
		slog.String("image", strategy.BaseImage()),
		slog.Int("port", result.Port),
	)
	return result, nil
}

// StopDevContainer stops and removes the dev container recorded on the sandbox.
// It is best-effort: failures are logged but do not block the caller. Callers
// should zero out DevContainerID/DevContainerPort on the sandbox row after this
// returns, regardless of error.
func (o *Orchestrator) StopDevContainer(ctx context.Context, sandbox *db.Sandbox) {
	if sandbox.DevContainerID == "" {
		return
	}
	cid := sandbox.DevContainerID

	stopCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	if err := o.StopContainer(stopCtx, cid); err != nil {
		if !dockerclient.IsErrNotFound(err) {
			o.logger.Warn("dev container: stop failed",
				slog.String("sandbox", sandbox.ID),
				slog.String("container", cid),
				slog.String("err", err.Error()))
		}
	}

	if err := o.RemoveContainer(ctx, cid); err != nil {
		if !dockerclient.IsErrNotFound(err) {
			o.logger.Warn("dev container: remove failed",
				slog.String("sandbox", sandbox.ID),
				slog.String("container", cid),
				slog.String("err", err.Error()))
		}
	}
}

// DevContainerHealthy returns true if the dev container's application port is
// accepting TCP connections right now. Used for polling from the preview-health
// endpoint.
func (o *Orchestrator) DevContainerHealthy(ctx context.Context, ip string, port int) bool {
	if ip == "" || port == 0 {
		return false
	}
	hctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return o.WaitHealthy(hctx, "", ip, port, 2*time.Second) == nil
}
