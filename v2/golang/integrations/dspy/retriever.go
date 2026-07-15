/*
Package dspy provides a retriever adapter for DSPy/LangChain Go pipelines.

Junior Tip: This bridges AnhurDB's Memory API to the standard
"retriever" interface used by Go agentic frameworks. It converts
SearchResult into Document structs that LangChainGo/DSPy expect.
*/
package dspy

import (
	"context"
	"fmt"

	"github.com/Yoven/AnhurDB-SDK/v2/golang/v2/client"
)

// Document represents a standard retrieved abstraction used in Go agentic frameworks.
type Document struct {
	ID          string
	PageContent string
	Metadata    map[string]interface{}
	Score       float64
}

// Retriever adheres to standard Go interfaces (like LangChainGo or DSPy equivalents).
//
// Junior Tip: It wraps the Memory client and translates search results
// into Document structs. Configure TargetType to filter by memory type
// (default: all types).
type Retriever struct {
	mem        *client.Memory
	TargetType string // optional: filter by memory type
	TopK       int
}

// NewRetriever initialises a semantic AnhurDB retriever.
//
// Junior Tip: k is the maximum number of documents to return per query.
func NewRetriever(mem *client.Memory, k int) *Retriever {
	return &Retriever{
		mem:  mem,
		TopK: k,
	}
}

// GetRelevantDocuments executes a search and returns Documents.
//
// Junior Tip: This is the method that LangChainGo/DSPy pipelines call.
// It maps AnhurDB SearchResult fields to Document fields automatically.
func (r *Retriever) GetRelevantDocuments(ctx context.Context, query string) ([]Document, error) {
	opts := []client.SearchOption{client.WithLimit(r.TopK)}
	if r.TargetType != "" {
		opts = append(opts, client.WithTypeFilter(r.TargetType))
	}

	results, err := r.mem.Search(ctx, query, opts...)
	if err != nil {
		return nil, fmt.Errorf("anhurdb retriever failed: %w", err)
	}

	docs := make([]Document, 0, len(results))
	for _, hit := range results {
		// SearchResult is now nested: the fields live on hit.Record (a full
		// models.Record) and the score stays at hit.Similarity. Record.Content is
		// `any` (the server may send a string body or a structured payload), so
		// take the string form when present and fall back to the summary.
		content, _ := hit.Record.Content.(string)
		if content == "" {
			content = hit.Record.Summary
		}

		doc := Document{
			ID:          fmt.Sprintf("%d", hit.Record.ID),
			PageContent: content,
			Score:       hit.Similarity,
			Metadata: map[string]interface{}{
				"type":     string(hit.Record.Type),
				"metadata": hit.Record.Metadata,
			},
		}
		docs = append(docs, doc)
	}

	return docs, nil
}
