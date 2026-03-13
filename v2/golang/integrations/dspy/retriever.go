package dspy

import (
	"context"
	"fmt"

	"github.com/yoven/anhurdb-sdk/v2/golang"
	"github.com/yoven/anhurdb-sdk/v2/golang/query"
)

// Document represents a standard retrieved abstraction used in Go agentic frameworks.
type Document struct {
	ID         string
	PageContent string
	Metadata   map[string]interface{}
	Score      float64
}

// Retriever adheres to standard Go interfaces (like LangChainGo or DSPy equivalents).
type Retriever struct {
	client     *anhurdb.AnhurClient
	TenantID   string
	TargetType string
	Mode       query.SemanticMode
	TopK       int
}

// NewRetriever initializes a semantic AnhurDB retriever for integration into DSPy/LangChain pipelines.
func NewRetriever(client *anhurdb.AnhurClient, tenantID string, k int) *Retriever {
	return &Retriever{
		client:     client,
		TenantID:   tenantID,
		TargetType: "fact", // Default to cognitive facts
		Mode:       query.ModeHybrid,
		TopK:       k,
	}
}

// GetRelevantDocuments executes the cognitive search against the Motor.
func (r *Retriever) GetRelevantDocuments(ctx context.Context, queryStr string) ([]Document, error) {
	b := r.client.Memories(ctx).
		Limit(r.TopK).
		SemanticSearch(queryStr, r.Mode)

	// Inject type filter safely
	if r.TargetType != "" {
		b = b.WhereEq("type", r.TargetType)
	}

	result, err := b.Execute()
	if err != nil {
		return nil, fmt.Errorf("anhurdb retriever failed: %w", err)
	}

	// Map generic abstract AST result into DSPy/LangChain Documents
	// In the real Anhur ecosystem, 'result' is map[string]interface{} containing 'results'
	var docs []Document
	if resultMap, ok := result.(map[string]interface{}); ok {
		if recordsRaw, exists := resultMap["results"]; exists {
			if records, ok := recordsRaw.([]interface{}); ok {
				for _, recRaw := range records {
					if rec, ok := recRaw.(map[string]interface{}); ok {
						doc := Document{
							ID:       fmt.Sprintf("%v", rec["id"]),
							Metadata: make(map[string]interface{}),
						}
						
						// Prefer full content, fallback to summary
						if content, ok := rec["content"].(string); ok && content != "" {
							doc.PageContent = content
						} else if summary, ok := rec["summary"].(string); ok {
							doc.PageContent = summary
						}

						if sim, ok := rec["similarity"].(float64); ok {
							doc.Score = sim
						}

						if meta, ok := rec["metadata"].(map[string]interface{}); ok {
							doc.Metadata = meta
						}
						
						docs = append(docs, doc)
					}
				}
			}
		}
	}
	
	return docs, nil
}
