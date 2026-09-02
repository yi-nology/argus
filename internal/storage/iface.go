package storage

import (
	"context"
	"time"

	"argus/internal/plugin"
)

// TaskStore is the storage abstraction used by runner, service, poster, and worker.
// *Store implements this interface; tests can substitute a mock.
type TaskStore interface {
	// GetTask retrieves a task by ID.
	GetTask(ctx context.Context, id string) (*Task, error)

	// CreateTaskForWebhook performs SHA-dedup check + insert in one transaction.
	// Non-empty dupReason means dedup was triggered ("last_head_sha" | "in_flight" | "event_id").
	CreateTaskForWebhook(ctx context.Context, t *Task) (dupReason, existingID string, err error)

	// FinishTask marks a task as finished with the given status, verdict, and metadata.
	FinishTask(ctx context.Context, id, status, verdict, report, resultJSON, failReason string, tokensUsed int, summaryCommentID int64) error

	// ClaimNextPending atomically claims the next pending task (pending -> running).
	// Returns ok=false when no pending tasks are available.
	ClaimNextPending(ctx context.Context) (*Task, bool, error)

	// ResetRunningToPending resets stale running tasks back to pending.
	ResetRunningToPending(ctx context.Context, staleAfter time.Duration) (int64, error)

	// TaskCounts returns queue depth (pending and running counts).
	TaskCounts(ctx context.Context) (pending, running int64, err error)

	// PruneTasks deletes finished tasks created before the cutoff time.
	PruneTasks(ctx context.Context, cutoff time.Time) (int64, error)

	// ListTasks returns tasks ordered by created_at DESC, filtered by status (empty = all).
	ListTasks(ctx context.Context, status string, limit int) ([]Task, error)

	// SuppressPrint marks a finding fingerprint as suppressed (false-positive feedback).
	SuppressPrint(ctx context.Context, platform, owner, repo, number, fingerprint string, f plugin.Finding, at time.Time) (matched bool, err error)

	// ResolvePrintFingerprint resolves a short prefix to the full fingerprint string.
	ResolvePrintFingerprint(ctx context.Context, platform, owner, repo, number, prefix string) (string, bool)

	// MonthlyTokensByRepo returns per-repo token usage since the given time.
	MonthlyTokensByRepo(ctx context.Context, since time.Time) ([]RepoTokenUsage, error)

	// SetTaskDebug stores debug artifacts for a task (ARGUS_DEBUG=1).
	SetTaskDebug(ctx context.Context, id, debugJSON string) error

	// Ping checks database connectivity.
	Ping(ctx context.Context) error
}

// Compile-time check: *Store satisfies TaskStore.
var _ TaskStore = (*Store)(nil)
