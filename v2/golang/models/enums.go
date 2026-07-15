/*
Package models defines the cognitive memory types and statuses for AnhurDB.

These constants match the canonical AnhurDB taxonomy; all three SDKs share
the same values.
*/
package models

// MemoryType defines the cognitive memory types in AnhurDB.
// Canonical AnhurDB taxonomy (12 types).
type MemoryType string

const (
	TypeEpisodic     MemoryType = "episodic"     // Raw conversation turns
	TypeFact         MemoryType = "fact"          // Verifiable information
	TypePreference   MemoryType = "preference"    // User likes/dislikes
	TypeDecision     MemoryType = "decision"      // Team or personal choices
	TypeTask         MemoryType = "task"          // Action items
	TypeRisk         MemoryType = "risk"          // Concerns and warnings
	TypeReasoning    MemoryType = "reasoning"     // Chain of thought
	TypeIdea         MemoryType = "idea"          // Proposals
	TypeEmotion      MemoryType = "emotion"       // Expressed feelings
	TypeConsolidated MemoryType = "consolidated"  // Agent-synthesised summary
	TypeHub          MemoryType = "hub"           // Cross-session cluster
	TypeFile         MemoryType = "file"          // Uploaded document root
)

// MemoryStatus defines the lifecycle status of a record.
//
// Sourced from:
//   - Go server: create.go, update.go, record_batch.go, upload.go
//   - Python agents: consolidator.py, judge.py, regression/worker.py
type MemoryStatus string

const (
	StatusSaved              MemoryStatus = "saved"               // Default on creation
	StatusPending            MemoryStatus = "pending"             // Legacy (pre-v2)
	StatusConsolidated       MemoryStatus = "consolidated"        // After consolidation
	StatusArchived           MemoryStatus = "archived"            // Soft-deleted
	StatusDecayed            MemoryStatus = "decayed"             // Memory decay applied
	StatusProcessing         MemoryStatus = "processing"          // Being ingested
	StatusCompleted          MemoryStatus = "completed"           // Ingestion done
	StatusLinked             MemoryStatus = "linked"              // Similarity edges set
	StatusHubbed             MemoryStatus = "hubbed"              // Assigned to hub node
	StatusFailed             MemoryStatus = "failed"              // Generic failure
	StatusPendingJudge       MemoryStatus = "pending_judge"       // Awaiting Judge
	StatusFailedJudge        MemoryStatus = "failed_judge"        // Judge rejected
	StatusFailedConsolidation MemoryStatus = "failed_consolidation" // Consolidation failed
)
