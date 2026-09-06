package tasks

import "context"

// RecoveryJob keeps a provider's node-local promotion separate from its leased
// database recovery. Application assembly installs these callbacks before the
// scheduler starts; importing the task package never registers background work.
type RecoveryJob struct {
	Name      string
	Promote   func(context.Context) error
	Reconcile func(context.Context) error
}

// AsyncRecoveryJobs preserves the established provider order. Veo recovery
// reconciles both Gemini and Vertex through their distinct provider descriptors.
func AsyncRecoveryJobs() []RecoveryJob {
	return []RecoveryJob{
		{"ali-wan", PromoteAliWanTaskRecoveryJournalsContext, reconcileAsyncAliWanTasks},
		{"doubao", PromoteDoubaoTaskRecoveryJournalsContext, reconcileAsyncDoubaoTasks},
		{"gemini-vertex-veo", PromoteGeminiVeoTaskRecoveryJournalsContext, reconcileAsyncGeminiVeoTasks},
		{"hailuo", PromoteHailuoTaskRecoveryJournalsContext, reconcileAsyncHailuoTasks},
		{"kling", PromoteKlingTaskRecoveryJournalsContext, reconcileAsyncKlingTasks},
		{"midjourney", PromoteMidjourneyTaskRecoveryJournalsContext, reconcileAsyncMidjourneyTasks},
		{"suno", PromoteSunoTaskRecoveryJournalsContext, reconcileAsyncSunoTasks},
		{"video", PromoteVideoTaskRecoveryJournalsContext, reconcileAsyncVideoTasks},
		{"vidu", PromoteViduTaskRecoveryJournalsContext, reconcileAsyncViduTasks},
	}
}

// JimengRecoveryJob uses database-only reconciliation: its node-local journal
// has already been promoted before the scheduler obtains the cluster lease.
func JimengRecoveryJob() RecoveryJob {
	return RecoveryJob{"jimeng", PromoteJimengTaskRecoveryContext, reconcileJimengTaskOperationsDatabaseContext}
}
