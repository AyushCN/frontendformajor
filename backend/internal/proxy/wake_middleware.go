// Package proxy contains the sandbox traffic gateway: the single place where
// requests for <subdomain>.sandbox.localhost arrive (routed there by Traefik's
// wildcard rule) and are forwarded to the correct container. Its key job is
// wake-on-request — restarting a hibernated container mid-request so the user
// experience of an idle sandbox is a few seconds of extra latency, never an
// error.
package proxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/api-sandbox-links/backend/internal/cache"
	"github.com/api-sandbox-links/backend/internal/config"
	"github.com/api-sandbox-links/backend/internal/db"
	"github.com/api-sandbox-links/backend/internal/orchestrator"
	"github.com/api-sandbox-links/backend/internal/secret"
)

// wakeTimeout is how long the gateway waits for a hibernated container to
// become healthy before giving up on the current request.
const wakeTimeout = 90 * time.Second

// wakeCall represents an in-flight wake operation shared across concurrent requests.
type wakeCall struct {
	done chan struct{}
	err  error
}

// Gateway reverse-proxies sandbox subdomain traffic to the matching container
// and performs on-demand wake-ups.
type Gateway struct {
	store   *db.Store
	cache   *cache.Redis
	orch    *orchestrator.Orchestrator
	cfg     *config.Config
	logger  *slog.Logger
	limiter *IPRateLimiter

	// waking guards per-sandbox wake-ups so concurrent requests to a
	// hibernated sandbox share a single wake operation and error outcome.
	wakingMu sync.Mutex
	waking   map[string]*wakeCall
}

// NewGateway builds the traffic gateway with its dependencies.
func NewGateway(store *db.Store, cache *cache.Redis, orch *orchestrator.Orchestrator, cfg *config.Config, logger *slog.Logger) *Gateway {
	rps := 100
	burst := 200
	if cfg != nil {
		if cfg.GatewayRateLimitRPS > 0 {
			rps = cfg.GatewayRateLimitRPS
		}
		if cfg.GatewayRateLimitBurst > 0 {
			burst = cfg.GatewayRateLimitBurst
		}
	}
	return &Gateway{
		store:   store,
		cache:   cache,
		orch:    orch,
		cfg:     cfg,
		logger:  logger,
		limiter: NewIPRateLimiter(rps, burst),
		waking:  map[string]*wakeCall{},
	}
}

// Limiter returns the active IPRateLimiter instance for metrics and status inspection.
func (g *Gateway) Limiter() *IPRateLimiter {
	return g.limiter
}

// Handler returns the reverse-proxy HTTP handler for sandbox subdomains.
func (g *Gateway) Handler() http.Handler {
	return http.HandlerFunc(g.serve)
}

// serve resolves the subdomain from the Host header, wakes the sandbox if it
// is hibernated, and proxies the request to the container over its private
// network address.
func (g *Gateway) serve(w http.ResponseWriter, r *http.Request) {
	// 1. Rate limiting per client IP
	cip := clientIP(r)
	if g.limiter != nil && !g.limiter.Allow(cip) {
		g.logger.Warn("gateway: rate limit exceeded", slog.String("client_ip", cip), slog.String("host", r.Host))
		w.Header().Set("Retry-After", "1")
		http.Error(w, "Rate limit exceeded. Please wait a moment.", http.StatusTooManyRequests)
		return
	}

	// 2. Request body size guard
	if g.cfg != nil && g.cfg.GatewayMaxBodyBytes > 0 && r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, g.cfg.GatewayMaxBodyBytes)
	}

	subdomain, err := g.subdomainFromHost(r.Host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// We show a "gateway still warming the sandbox" page while waking instead
	// of queueing indefinitely; this is the one place a subdomain may not yet
	// exist synchronously.
	sandbox, err := g.store.GetSandboxBySubdomain(subdomain)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// A destroyed or expired sandbox answers with 410 Gone: the runtime once
	// existed and cannot be revived.
	isExpired := sandbox.DestroyedAt != nil || sandbox.Status == db.StatusExpired ||
		(sandbox.Status == db.StatusHibernated && !sandbox.ExpiresAt.IsZero() && sandbox.ExpiresAt.After(time.Unix(0, 0)) && time.Now().After(sandbox.ExpiresAt))

	if isExpired {
		http.Error(w, "This sandbox has expired and cannot be revived. Persisted data remains accessible according to project retention policy.", http.StatusGone)
		return
	}

	if sandbox.ContainerID == "" {
		http.Error(w, "This sandbox has no running container (it may still be building).", http.StatusServiceUnavailable)
		return
	}

	// Wake-on-request: a hibernated container gets restarted before we forward.
	if sandbox.Status == db.StatusHibernated {
		// Authenticate and authorize wake-up:
		user, authErr := g.authenticateRequest(r)
		if authErr != nil || user == nil {
			http.Error(w, "Authentication required to wake this hibernated sandbox.", http.StatusUnauthorized)
			return
		}
		canAccess, err := g.store.CanAccessSandbox(sandbox.ID, user.ID)
		if err != nil || !canAccess {
			http.Error(w, "Access denied: you must be an authorized project member to wake this sandbox.", http.StatusForbidden)
			return
		}

		// Readiness check query (?readiness=1 or Accept: application/json)
		if r.URL.Query().Get("readiness") == "1" || (r.Header.Get("Accept") == "application/json" && r.Method == http.MethodGet) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(fmt.Sprintf(`{"status":"WAKING","ready":false,"subdomain":"%s","message":"Sandbox runtime is waking up..."}`, sandbox.Subdomain)))
			go func() {
				_ = g.wake(context.Background(), sandbox)
			}()
			return
		}

		if err := g.wake(r.Context(), sandbox); err != nil {
			g.logger.Warn("gateway: wake failed", slog.String("sandbox", sandbox.ID), slog.String("err", err.Error()))
			if errors.Is(err, context.Canceled) {
				return
			}
			if errors.Is(err, context.DeadlineExceeded) {
				http.Error(w, "Sandbox timed out while waking up.", http.StatusGatewayTimeout)
				return
			}
			http.Error(w, fmt.Sprintf("Sandbox failed to wake: %s", err.Error()), http.StatusServiceUnavailable)
			return
		}
	}

	// Mark activity only for meaningful user requests so the idle reaper's timer restarts.
	// Filter out synthetic traffic (health probes, pings, dev HMR, favicons, websocket heartbeats).
	if !isSyntheticTraffic(r.URL.Path) {
		userID := ""
		if user, err := g.authenticateRequest(r); err == nil && user != nil {
			userID = user.ID
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = g.cache.TouchUserActivity(ctx, sandbox.ID, userID, sandbox.IdleTimeoutSeconds)
		}()
	}

	ip, err := g.orch.ContainerIP(r.Context(), sandbox.ContainerID, "sandbox_"+sandbox.ID[:8])
	if err != nil {
		http.Error(w, "Sandbox container is unreachable.", http.StatusServiceUnavailable)
		return
	}

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = "http"
			req.URL.Host = fmt.Sprintf("%s:%d", ip, sandbox.InternalPort)
			// Tell the app which host the user actually typed so redirects and
			// absolute links keep working from outside.
			req.Header.Set("X-Forwarded-Host", r.Host)
			req.Host = fmt.Sprintf("%s:%d", ip, sandbox.InternalPort)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			g.logger.Warn("gateway: proxy error", slog.String("sandbox", sandbox.ID), slog.String("err", err.Error()))
			http.Error(w, "Sandbox container is not responding.", http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r)
}

// wake restarts a hibernated container and waits for it to be healthy. It is
// concurrency-safe: concurrent requests to the same hibernated sandbox share
// a single wake operation, and the resulting error or success is broadcast
// to all awaiting callers.
func (g *Gateway) wake(ctx context.Context, sandbox *db.Sandbox) error {
	g.wakingMu.Lock()
	if call, ok := g.waking[sandbox.ID]; ok {
		g.wakingMu.Unlock()
		select {
		case <-call.done:
			return call.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	call := &wakeCall{
		done: make(chan struct{}),
	}
	g.waking[sandbox.ID] = call
	g.wakingMu.Unlock()

	go func() {
		// Run actual wake with a detached context bounded by wakeTimeout
		// so an individual caller disconnecting does not abort an in-flight wake for others.
		wakeCtx, cancel := context.WithTimeout(context.Background(), wakeTimeout)
		defer cancel()

		err := g.executeWake(wakeCtx, sandbox)

		g.wakingMu.Lock()
		call.err = err
		delete(g.waking, sandbox.ID)
		close(call.done)
		g.wakingMu.Unlock()
	}()

	select {
	case <-call.done:
		return call.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *Gateway) executeWake(ctx context.Context, sandbox *db.Sandbox) error {
	start := time.Now()
	if err := g.orch.StartContainer(ctx, sandbox.ContainerID); err != nil {
		return fmt.Errorf("starting container: %w", err)
	}

	ip, err := g.orch.ContainerIP(ctx, sandbox.ContainerID, "sandbox_"+sandbox.ID[:8])
	if err != nil {
		return fmt.Errorf("resolving container IP: %w", err)
	}
	if err := g.orch.WaitHealthy(ctx, sandbox.ContainerID, ip, sandbox.InternalPort, wakeTimeout); err != nil {
		return fmt.Errorf("waiting for healthy container: %w", err)
	}

	// The sandbox is up again; it is not hibernated anymore. Restart its stats
	// collector so the dashboard keeps flowing.
	g.orch.EnsureStatsCollector(sandbox.ID, sandbox.ContainerID)
	if err := g.store.TransitionToRunning(sandbox.ID); err != nil {
		return fmt.Errorf("marking running: %w", err)
	}
	g.logger.Info("gateway: sandbox woke successfully",
		slog.String("sandbox", sandbox.ID),
		slog.Duration("elapsed", time.Since(start)),
	)
	return nil
}

// subdomainFromHost strips the base domain from a Host header, returning the
// sandbox's subdomain. It accepts host[:port] forms.
func (g *Gateway) subdomainFromHost(host string) (string, error) {
	host = strings.TrimSpace(host)
	if i := strings.LastIndex(host, ":"); i != -1 {
		host = host[:i] // strip port
	}
	if !strings.HasSuffix(host, "."+g.cfg.SandboxBaseDomain) {
		return "", fmt.Errorf("unrecognized sandbox host %q", host)
	}
	sub := strings.TrimSuffix(host, "."+g.cfg.SandboxBaseDomain)
	if sub == "" || strings.ContainsAny(sub, " ") {
		return "", fmt.Errorf("malformed sandbox host %q", host)
	}
	return sub, nil
}

func (g *Gateway) authenticateRequest(r *http.Request) (*db.User, error) {
	cookie, err := r.Cookie("asl_session")
	if err != nil {
		return nil, fmt.Errorf("no session cookie found")
	}
	if g.cfg != nil && len(g.cfg.EncryptionKey) == 32 {
		b, err := secret.NewBox(g.cfg.EncryptionKey)
		if err == nil {
			plain, err := b.Decrypt([]byte(cookie.Value))
			if err == nil {
				githubID, err := strconv.ParseInt(string(plain), 10, 64)
				if err == nil {
					return g.store.GetUserByGithubID(githubID)
				}
			}
		}
	}
	return nil, fmt.Errorf("session authentication failed")
}

// isSyntheticTraffic detects health checks, pings, dev assets, and framework polling.
func isSyntheticTraffic(reqPath string) bool {
	return reqPath == "/favicon.ico" || reqPath == "/robots.txt" ||
		strings.HasPrefix(reqPath, "/health") || strings.HasPrefix(reqPath, "/ping") ||
		strings.HasPrefix(reqPath, "/__webpack") || strings.HasPrefix(reqPath, "/_next")
}
