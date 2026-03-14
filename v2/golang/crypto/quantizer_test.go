package crypto_test

import (
	"testing"
	"github.com/yoven/anhurdb-sdk/v2/golang/v2/crypto"
)

func TestCosineSimilarity(t *testing.T) {
	// Stub test
	res := crypto.CosineSimilarity([]float64{1.0}, []float64{1.0})
	if res != 0.0 {
		t.Errorf("Expected 0.0, got %f", res)
	}
}
