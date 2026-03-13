package models

type SessionStats struct {
	UUID        string         `json:"uuid"`
	RecordCount int            `json:"record_count"`
	Types       map[string]int `json:"types"`
	LastActivity string        `json:"last_activity"`
	Summary     *string        `json:"summary,omitempty"`
}
