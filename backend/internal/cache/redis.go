// Package cache wraps Redis, the platform's ephemeral/hot-state store.
//
// Redis deliberately holds only two kinds of data (see BUILD_PROMPT section 2):
//  1. last-request markers used by the idle-timeout reaper,
//  2. the latest resource sample per sandbox, flushed to Postgres on a timer.
//
// Everything durable lives in Postgres; nothing in Redis is an authoritative
// record of anything.
package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis is the typed wrapper around a go-redis client.
type Redis struct {
	client *redis.Client
}

// New connects to Redis and verifies connectivity with a ping.
func New(ctx context.Context, url string) (*Redis, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("cache: parsing redis url: %w", err)
	}
	client := redis.NewClient(opt)
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("cache: unable to reach redis: %w", err)
	}
	return &Redis{client: client}, nil
}

// Ping verifies connectivity to Redis.
func (r *Redis) Ping(ctx context.Context) error {
	if r == nil || r.client == nil {
		return fmt.Errorf("cache: redis client not initialized")
	}
	return r.client.Ping(ctx).Err()
}

// ---- last-request-at markers (idle timeout) -----------------------------
//
// Each request through the sandbox gateway calls Touch on the sandbox's key
// with a TTL equal to its idle timeout. The idle reaper hibernates a running
// sandbox when its key is absent — i.e. no request has bumped it within the
// timeout window. This TTL-as-timer trick means the reaper needs no stored
// timestamps and no clock skew issues.

func lastRequestKey(sandboxID string) string {
	return "lr:" + sandboxID
}

// Touch marks sandboxID as active for the next ttlSeconds. Called on every
// proxied request (hot path; keep cheap).
func (r *Redis) Touch(ctx context.Context, sandboxID string, ttlSeconds int) error {
	if err := r.client.Set(ctx, lastRequestKey(sandboxID), time.Now().Unix(), time.Duration(ttlSeconds)*time.Second).Err(); err != nil {
		return fmt.Errorf("cache: touching last-request marker: %w", err)
	}
	return nil
}

// IsIdle reports whether no request has touched the sandbox's marker, meaning
// it has likely been idle at least as long as its configured timeout.
func (r *Redis) IsIdle(ctx context.Context, sandboxID string) bool {
	n, err := r.client.Exists(ctx, lastRequestKey(sandboxID)).Result()
	if err != nil {
		return false
	}
	return n == 0
}

// Clear removes a sandbox's marker (used when hibernating so a fresh wake is
// counted as brand-new activity).
func (r *Redis) Clear(ctx context.Context, sandboxID string) error {
	if r == nil || r.client == nil {
		return nil
	}
	_ = r.ClearHibernationState(ctx, sandboxID)
	// Clear all user activity keys for this sandbox
	pattern := fmt.Sprintf("act:%s:*", sandboxID)
	if keys, err := r.client.Keys(ctx, pattern).Result(); err == nil && len(keys) > 0 {
		_ = r.client.Del(ctx, keys...).Err()
	}
	return r.client.Del(ctx, lastRequestKey(sandboxID)).Err()
}

// ---- Multi-user activity & active operations ----------------------------

func userActivityKey(sandboxID, userID string) string {
	return fmt.Sprintf("act:%s:%s", sandboxID, userID)
}

func activeOperationKey(sandboxID string) string {
	return "op:" + sandboxID
}

func hibernationWarningKey(sandboxID string) string {
	return "warn:hibernation:" + sandboxID
}

func hibernationGraceKey(sandboxID string) string {
	return "grace:hibernation:" + sandboxID
}

// ActiveOperation holds metadata about an in-flight operation that temporarily
// protects a sandbox from idle hibernation.
type ActiveOperation struct {
	Type               string    `json:"type"`
	UserID             string    `json:"user_id"`
	StartedAt          time.Time `json:"started_at"`
	MaxDurationSeconds int64     `json:"max_duration_seconds"`
}

// AllowedOperationTypes are valid, recognized bounded operations.
var AllowedOperationTypes = map[string]bool{
	"BUILD":              true,
	"DEPENDENCY_INSTALL": true,
	"GIT_PULL":           true,
	"GIT_COMMIT":         true,
	"UPLOAD":             true,
	"MIGRATION":          true,
}

// TouchUserActivity records activity for a specific authorized user.
// Bumps both the per-user activity key and the aggregate last-request key.
// Also clears any active hibernation warning or grace period.
func (r *Redis) TouchUserActivity(ctx context.Context, sandboxID, userID string, ttlSeconds int) error {
	if r == nil || r.client == nil {
		return nil
	}
	dur := time.Duration(ttlSeconds) * time.Second
	now := time.Now().Unix()

	pipe := r.client.Pipeline()
	pipe.Set(ctx, lastRequestKey(sandboxID), now, dur)
	if userID != "" {
		pipe.Set(ctx, userActivityKey(sandboxID, userID), now, dur)
	}
	pipe.Del(ctx, hibernationWarningKey(sandboxID))
	pipe.Del(ctx, hibernationGraceKey(sandboxID))
	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("cache: touching user activity: %w", err)
	}
	return nil
}

// GetIdleTTL returns the remaining TTL of the sandbox's idle timer, or 0 if expired/idle.
func (r *Redis) GetIdleTTL(ctx context.Context, sandboxID string) time.Duration {
	if r == nil || r.client == nil {
		return 0
	}
	ttl, err := r.client.TTL(ctx, lastRequestKey(sandboxID)).Result()
	if err != nil || ttl < 0 {
		return 0
	}
	return ttl
}

// HasActiveUserActivity checks if ANY authorized user is currently active on the sandbox.
func (r *Redis) HasActiveUserActivity(ctx context.Context, sandboxID string) bool {
	if r == nil || r.client == nil {
		return false
	}
	pattern := fmt.Sprintf("act:%s:*", sandboxID)
	keys, err := r.client.Keys(ctx, pattern).Result()
	if err != nil {
		return false
	}
	return len(keys) > 0
}

// StartOperation registers a bounded active operation.
func (r *Redis) StartOperation(ctx context.Context, sandboxID string, opType string, userID string, maxDuration time.Duration) error {
	if r == nil || r.client == nil {
		return nil
	}
	if !AllowedOperationTypes[opType] {
		return fmt.Errorf("invalid operation type: %s", opType)
	}
	if maxDuration <= 0 || maxDuration > 30*time.Minute {
		maxDuration = 15 * time.Minute // default 15 min bound
	}
	op := ActiveOperation{
		Type:               opType,
		UserID:             userID,
		StartedAt:          time.Now().UTC(),
		MaxDurationSeconds: int64(maxDuration.Seconds()),
	}
	data, err := json.Marshal(op)
	if err != nil {
		return err
	}
	return r.client.Set(ctx, activeOperationKey(sandboxID), data, maxDuration).Err()
}

// GetActiveOperation checks if an active operation is running and not expired.
func (r *Redis) GetActiveOperation(ctx context.Context, sandboxID string) (*ActiveOperation, bool) {
	if r == nil || r.client == nil {
		return nil, false
	}
	data, err := r.client.Get(ctx, activeOperationKey(sandboxID)).Bytes()
	if err != nil {
		return nil, false
	}
	var op ActiveOperation
	if err := json.Unmarshal(data, &op); err != nil {
		_ = r.client.Del(ctx, activeOperationKey(sandboxID))
		return nil, false
	}
	// Verify bounded duration
	if time.Since(op.StartedAt) > time.Duration(op.MaxDurationSeconds)*time.Second {
		_ = r.client.Del(ctx, activeOperationKey(sandboxID))
		return nil, false
	}
	return &op, true
}

// EndOperation completes the active operation.
func (r *Redis) EndOperation(ctx context.Context, sandboxID string) error {
	if r == nil || r.client == nil {
		return nil
	}
	return r.client.Del(ctx, activeOperationKey(sandboxID)).Err()
}

// SetHibernationWarning flags a sandbox approaching hibernation.
func (r *Redis) SetHibernationWarning(ctx context.Context, sandboxID string, ttl time.Duration) error {
	if r == nil || r.client == nil {
		return nil
	}
	return r.client.Set(ctx, hibernationWarningKey(sandboxID), time.Now().Unix(), ttl).Err()
}

// HasHibernationWarning checks if the warning flag is set.
func (r *Redis) HasHibernationWarning(ctx context.Context, sandboxID string) bool {
	if r == nil || r.client == nil {
		return false
	}
	n, err := r.client.Exists(ctx, hibernationWarningKey(sandboxID)).Result()
	return err == nil && n > 0
}

// SetGracePeriod starts the grace period countdown before hibernation.
func (r *Redis) SetGracePeriod(ctx context.Context, sandboxID string, duration time.Duration) error {
	if r == nil || r.client == nil {
		return nil
	}
	return r.client.Set(ctx, hibernationGraceKey(sandboxID), time.Now().Unix(), duration).Err()
}

// InGracePeriod checks if grace period is active.
func (r *Redis) InGracePeriod(ctx context.Context, sandboxID string) bool {
	if r == nil || r.client == nil {
		return false
	}
	n, err := r.client.Exists(ctx, hibernationGraceKey(sandboxID)).Result()
	return err == nil && n > 0
}

// ClearHibernationState cleans up warning and grace keys.
func (r *Redis) ClearHibernationState(ctx context.Context, sandboxID string) error {
	if r == nil || r.client == nil {
		return nil
	}
	return r.client.Del(ctx, hibernationWarningKey(sandboxID), hibernationGraceKey(sandboxID)).Err()
}

// ClearLatestStat removes the sandbox's latest sample from Redis during teardown.
func (r *Redis) ClearLatestStat(ctx context.Context, sandboxID string) error {
	return r.client.Del(ctx, latestStatKey(sandboxID)).Err()
}

// ---- latest live resource sample (resource dashboard) -------------------
//
// The stats collector overwrites one key per sandbox on every sample, and the
// flush worker reads it on its own slower cadence to build the history chart.
// A single key rather than a growing list, because the 1s feed exists to drive
// a live readout and only its newest value is ever worth persisting.

func latestStatKey(sandboxID string) string {
	return "stats:latest:" + sandboxID
}

// latestStatTTL is deliberately short. If a container's stats stream dies, the
// key must expire rather than serve a frozen reading as though it were live.
const latestStatTTL = 10 * time.Second

// SetLatestStat overwrites the sandbox's most recent sample. The TTL is
// refreshed on every write, so the key's lifetime tracks the collector's.
func (r *Redis) SetLatestStat(ctx context.Context, sandboxID string, sample []byte) error {
	if err := r.client.Set(ctx, latestStatKey(sandboxID), sample, latestStatTTL).Err(); err != nil {
		return fmt.Errorf("cache: writing latest stat: %w", err)
	}
	return nil
}

// LatestStat returns the sandbox's most recent sample, or nil when the key is
// absent or has expired.
func (r *Redis) LatestStat(ctx context.Context, sandboxID string) ([]byte, error) {
	raw, err := r.client.Get(ctx, latestStatKey(sandboxID)).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cache: reading latest stat: %w", err)
	}
	return raw, nil
}

// StatFrame is the JSON shape published in Redis between the stats collector
// and the Postgres flush worker.
type StatFrame struct {
	SandboxID        string    `json:"sandbox_id"`
	CPUPercent       float32   `json:"cpu_percent"`
	MemoryUsedBytes  int64     `json:"memory_used_bytes"`
	MemoryLimitBytes int64     `json:"memory_limit_bytes"`
	NetworkRxBytes   int64     `json:"network_rx_bytes"`
	NetworkTxBytes   int64     `json:"network_tx_bytes"`
	RecordedAt       time.Time `json:"recorded_at"`
}

// EncodeStatFrame renders a sample for publishing.
func EncodeStatFrame(f StatFrame) ([]byte, error) {
	return json.Marshal(f)
}

// DecodeStatFrame parses a published sample.
func DecodeStatFrame(raw []byte) (*StatFrame, error) {
	var f StatFrame
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("cache: decoding published stat sample: %w", err)
	}
	return &f, nil
}
