/*
Package models defines the cognitive memory types and statuses for AnhurDB.

These constants match the canonical AnhurDB taxonomy; all three SDKs share
the same values.
*/
package models

// MemoryType defines the cognitive memory types in AnhurDB.
// Canonical AnhurDB taxonomy (13 types).
//
// Junior Tip [taxonomy SSOT]: this list is NOT free-form. Its authority is
// AnhurCore's core.yaml, mirrored by the server's schema.MemoryTypes and by the
// MCP list_types tool. A type declared here but absent server-side is rejected
// with HTTP 400 on create; a type served by the server but missing here silently
// shrinks ListTypes() and — in TypeScript — makes the value a compile error for
// the caller. Keep all three SDKs and core.yaml in step, in the same change.
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
	TypeRouter       MemoryType = "router"        // Macro-theme backbone: a hub of hubs (ADR-0005)
)

// MemoryStatus defines the lifecycle status of a record.
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
