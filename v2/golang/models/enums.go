package models

// MemoryType defines the cognitive memory types in AnhurDB.
type MemoryType string

const (
	TypeEpisodic   MemoryType = "episodic"
	TypeFact       MemoryType = "fact"
	TypeReasoning  MemoryType = "reasoning"
	TypeDecision   MemoryType = "decision"
	TypeRisk       MemoryType = "risk"
	TypePreference MemoryType = "preference"
	TypeHub        MemoryType = "hub"
	TypeIdea       MemoryType = "idea"
	TypeEntity     MemoryType = "entity"
)

// MemoryStatus defines the synchronization status of a record.
type MemoryStatus string

const (
	StatusSaved        MemoryStatus = "saved"
	StatusConsolidated MemoryStatus = "consolidated"
	StatusArchived     MemoryStatus = "archived"
	StatusDecayed      MemoryStatus = "decayed"
	StatusProcessing   MemoryStatus = "processing"
	StatusFailed       MemoryStatus = "failed"
)
