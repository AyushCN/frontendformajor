// Package api implements the REST + WebSocket surface of the backend. Handlers
// stay thin: they validate/authorize requests, then delegate to the store,
// orchestrator, deployer, and other internal packages.
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/api-sandbox-links/backend/internal/db"
	gh "github.com/google/go-github/v66/github"
	"github.com/google/uuid"
	"golang.org/x/oauth2"
	oauth2github "golang.org/x/oauth2/github"
)

// oauthStateCookie is the session cookie name. The value is an AES-encrypted
// GitHub numeric ID; decryption doubles as authenticity (attacker-crafted
// cookies fail authentication). See secret.Box.
const (
	oauthStateCookie = "asl_session"
	oauthCsrfCookie  = "asl_oauth_state"
)

type ctxKey int

const (
	ctxUserKey ctxKey = iota
)

// githubOAuthConfig builds the oauth2 config from server config. It returns
// nil when GitHub OAuth is not configured (DevAuth mode).
func (s *Server) githubOAuthConfig() *oauth2.Config {
	if s.cfg.GitHubClientID == "" {
		return nil
	}
	return &oauth2.Config{
		ClientID:     s.cfg.GitHubClientID,
		ClientSecret: s.cfg.GitHubClientSecret,
		RedirectURL:  s.cfg.GitHubOAuthCallbackURL,
		Scopes:       []string{"read:user", "user:email", "repo"},
		Endpoint:     oauth2github.Endpoint,
	}
}

// handleAuthStart redirects the user to GitHub's consent screen.
func (s *Server) handleAuthStart(w http.ResponseWriter, r *http.Request) {
	cfg := s.githubOAuthConfig()
	if cfg == nil {
		http.Error(w, "GitHub OAuth is not configured (GITHUB_CLIENT_ID missing). DevAuth is active; try the health endpoint.", http.StatusNotImplemented)
		return
	}
	// Generate cryptographically random state for CSRF protection
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		http.Error(w, "could not generate secure state", http.StatusInternalServerError)
		return
	}
	state := hex.EncodeToString(b)

	isSecure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" || strings.HasPrefix(s.cfg.FrontendURL, "https://")
	http.SetCookie(w, &http.Cookie{
		Name:     oauthCsrfCookie,
		Value:    state,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isSecure,
		MaxAge:   600, // 10 minutes
	})

	url := cfg.AuthCodeURL(state, oauth2.AccessTypeOffline)
	http.Redirect(w, r, url, http.StatusFound)
}

// handleAuthCallback exchanges the code, fetches the GitHub profile, upserts
// the user, and sets the session cookie.
func (s *Server) handleAuthCallback(w http.ResponseWriter, r *http.Request) {
	cfg := s.githubOAuthConfig()
	if cfg == nil {
		http.Error(w, "OAuth not configured", http.StatusNotImplemented)
		return
	}

	// 1. Verify CSRF state against cookie
	stateQuery := r.URL.Query().Get("state")
	stateCookie, err := r.Cookie(oauthCsrfCookie)
	if err != nil || stateCookie.Value == "" || stateQuery == "" || stateCookie.Value != stateQuery {
		s.logger.Warn("auth: state mismatch or missing state cookie", slog.String("query_state", stateQuery))
		http.Error(w, "Invalid or expired OAuth state parameter (CSRF detected)", http.StatusBadRequest)
		return
	}

	// Clear CSRF state cookie
	isSecure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" || strings.HasPrefix(s.cfg.FrontendURL, "https://")
	http.SetCookie(w, &http.Cookie{
		Name:     oauthCsrfCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isSecure,
		MaxAge:   -1,
	})

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing OAuth code", http.StatusBadRequest)
		return
	}

	tok, err := cfg.Exchange(r.Context(), code)
	if err != nil {
		s.logger.Warn("auth: token exchange failed", slog.String("err", err.Error()))
		http.Error(w, "GitHub token exchange failed", http.StatusUnauthorized)
		return
	}

	profile, err := s.fetchGithubUser(r.Context(), tok.AccessToken)
	if err != nil {
		s.logger.Warn("auth: profile fetch failed", slog.String("err", err.Error()))
		http.Error(w, "Could not fetch GitHub profile", http.StatusBadGateway)
		return
	}

	encToken, err := s.box.Encrypt([]byte(tok.AccessToken))
	if err != nil {
		http.Error(w, "could not store access token", http.StatusInternalServerError)
		return
	}

	user := &db.User{
		GithubID:    profile.GithubID,
		Username:    profile.Login,
		Email:       s.truncate(profile.Email, 512),
		AvatarURL:   s.truncate(profile.AvatarURL, 1024),
		GithubToken: encToken,
	}
	if err := s.store.UpsertUser(user); err != nil {
		s.logger.Warn("auth: upserting user failed", slog.String("err", err.Error()))
		http.Error(w, "could not store user", http.StatusInternalServerError)
		return
	}

	// Ensure the user's default personal project space exists
	if createdUser, err := s.store.GetUserByGithubID(user.GithubID); err == nil && createdUser != nil {
		_, _ = s.store.GetOrCreateDefaultProject(createdUser.ID)
	}

	// Session payload format: <githubID>:<issuedAtUnix>:<sessionNonce>
	nonce := uuid.NewString()
	payload := fmt.Sprintf("%d:%d:%s", user.GithubID, time.Now().Unix(), nonce)
	session, err := s.box.Encrypt([]byte(payload))
	if err != nil {
		http.Error(w, "could not create session", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     oauthStateCookie,
		Value:    string(session),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isSecure,
		MaxAge:   30 * 24 * 3600, // 30 days
	})
	http.Redirect(w, r, s.cfg.FrontendURL+"/dashboard?authed=1", http.StatusFound)
}

// handleMe returns the authoritative current signed-in user identity contract.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	user, ok := s.userFrom(r)
	if !ok {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":         user.ID,
		"github_id":  user.GithubID,
		"username":   user.Username,
		"email":      user.Email,
		"avatar_url": user.AvatarURL,
		"dev":        s.cfg.DevAuth,
	})
}

// handleLogout clears the session cookie and blacklists the session nonce in Redis.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	// If a valid session cookie exists, extract nonce and revoke in cache
	if cookie, err := r.Cookie(oauthStateCookie); err == nil && cookie.Value != "" {
		if plain, dErr := s.box.Decrypt([]byte(cookie.Value)); dErr == nil {
			parts := strings.Split(string(plain), ":")
			if len(parts) >= 3 && s.cache != nil {
				_ = s.cache.RevokeSession(r.Context(), parts[2], 30*24*time.Hour)
			}
		}
	}

	isSecure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" || strings.HasPrefix(s.cfg.FrontendURL, "https://")
	http.SetCookie(w, &http.Cookie{
		Name:     oauthStateCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isSecure,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// requireUser is middleware ensuring a session exists; it resolves the user
// from the signed session cookie and stashes the *db.User in context.
func (s *Server) requireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, err := s.sessionUser(r)
		if err != nil {
			http.Error(w, "not signed in", http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), ctxUserKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// sessionUser decodes, authenticates, and validates the session cookie against expiration and revocation.
func (s *Server) sessionUser(r *http.Request) (*db.User, error) {
	cookie, err := r.Cookie(oauthStateCookie)
	if err != nil {
		return nil, err
	}
	plain, err := s.box.Decrypt([]byte(cookie.Value))
	if err != nil {
		return nil, fmt.Errorf("invalid session cookie: %w", err)
	}
	parts := strings.Split(string(plain), ":")
	githubID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("malformed session: %w", err)
	}

	if len(parts) >= 3 {
		// Verify expiration (30 days max lifetime)
		if issuedAt, pErr := strconv.ParseInt(parts[1], 10, 64); pErr == nil {
			if time.Since(time.Unix(issuedAt, 0)) > 30*24*time.Hour {
				return nil, fmt.Errorf("session expired")
			}
		}
		// Verify revocation
		sessionNonce := parts[2]
		if s.cache != nil && s.cache.IsSessionRevoked(r.Context(), sessionNonce) {
			return nil, fmt.Errorf("session revoked")
		}
	}

	user, err := s.store.GetUserByGithubID(githubID)
	if err != nil {
		return nil, err
	}
	return user, nil
}

// userFrom pulls the authenticated user out of the request context.
func (s *Server) userFrom(r *http.Request) (*db.User, bool) {
	u, ok := r.Context().Value(ctxUserKey).(*db.User)
	return u, ok
}

func (s *Server) truncate(v string, n int) string {
	if len(v) > n {
		return v[:n]
	}
	return v
}

// githubProfile is the minimal subset of go-github's User we read.
type githubProfile struct {
	GithubID  int64
	Email     string
	AvatarURL string
	Login     string
}

// fetchGithubUser calls GitHub's /user endpoint with the OAuth token using the
// official go-github client.
func (s *Server) fetchGithubUser(ctx context.Context, token string) (*githubProfile, error) {
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	client := gh.NewClient(oauth2.NewClient(ctx, ts))
	ghUser, _, err := client.Users.Get(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("fetching github user: %w", err)
	}
	return &githubProfile{
		GithubID:  ghUser.GetID(),
		Email:     ghUser.GetEmail(),
		AvatarURL: ghUser.GetAvatarURL(),
		Login:     ghUser.GetLogin(),
	}, nil
}

func (s *Server) handleDevLogin(w http.ResponseWriter, r *http.Request) {
	if s.cfg.IsProduction() || !s.cfg.DevAuth {
		http.Error(w, "dev auth disabled in production", http.StatusForbidden)
		return
	}
	var req struct {
		Username string `json:"username"`
		Email    string `json:"email"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	uname := strings.TrimSpace(req.Username)
	if uname == "" {
		uname = os.Getenv("DEV_USER_USERNAME")
	}
	if uname == "" {
		uname = "developer"
	}
	email := strings.TrimSpace(req.Email)
	if email == "" {
		email = os.Getenv("DEV_USER_EMAIL")
	}
	if email == "" {
		email = fmt.Sprintf("%s@users.api-sandbox.local", strings.ToLower(uname))
	}

	// Compute deterministic non-fixed positive int64 from username
	h := fnv.New64a()
	h.Write([]byte(uname))
	githubID := int64(h.Sum64() & 0x7FFFFFFFFFFFFFFF)
	if githubID == 0 {
		githubID = 1001
	}

	userID := uuid.NewString()
	if existing, err := s.store.GetUserByGithubID(githubID); err == nil && existing != nil {
		userID = existing.ID
	}

	encToken, _ := s.box.Encrypt([]byte(uname))
	user := db.User{
		ID:          userID,
		GithubID:    githubID,
		Username:    uname,
		Email:       email,
		AvatarURL:   fmt.Sprintf("https://github.com/%s.png", uname),
		GithubToken: encToken,
	}
	if err := s.store.UpsertUser(&user); err != nil {
		s.logger.Warn("dev auth: could not upsert user", slog.String("err", err.Error()))
	}

	// Ensure personal default space exists
	_, _ = s.store.GetOrCreateDefaultProject(user.ID)

	session, err := s.box.Encrypt([]byte(strconv.FormatInt(user.GithubID, 10)))
	if err != nil {
		http.Error(w, "could not generate session", http.StatusInternalServerError)
		return
	}
	isSecure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	http.SetCookie(w, &http.Cookie{
		Name:     oauthStateCookie,
		Value:    string(session),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isSecure,
		MaxAge:   30 * 24 * 3600, // 30 days
	})
	// Return sanitized user contract without raw tokens or secrets
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"user": map[string]any{
			"id":         user.ID,
			"github_id":  user.GithubID,
			"username":   user.Username,
			"email":      user.Email,
			"avatar_url": user.AvatarURL,
		},
	})
}

// handleSearchUsers searches registered users by username or email.
func (s *Server) handleSearchUsers(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) < 2 {
		writeJSON(w, http.StatusOK, map[string]any{"users": []any{}})
		return
	}
	users, err := s.store.SearchUsers(q, 10)
	if err != nil {
		s.logger.Warn("search users failed", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "failed to search users")
		return
	}
	results := make([]map[string]any, 0, len(users))
	for _, u := range users {
		results = append(results, map[string]any{
			"id":        u.ID,
			"username":  u.Username,
			"email":     u.Email,
			"avatarUrl": u.AvatarURL,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": results})
}
