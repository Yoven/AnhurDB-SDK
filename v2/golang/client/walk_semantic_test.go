package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// captureWalkBody stands up an httptest server that records the JSON body of the
// single /api/v1/walk/semantic request and replies with a minimal valid walk
// envelope. It returns the decoded body captured during the call.
//
func captureWalkBody(t *testing.T, startID int64, depth int, opts ...ReadOption) map[string]interface{} {
	t.Helper()

	var capturedBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		rawBody, readErr := io.ReadAll(request.Body)
		if readErr != nil {
			t.Fatalf("reading request body: %v", readErr)
		}
		if unmarshalErr := json.Unmarshal(rawBody, &capturedBody); unmarshalErr != nil {
			t.Fatalf("request body is not valid JSON: %v (%s)", unmarshalErr, rawBody)
		}
		responseWriter.Header().Set("Content-Type", "application/json")
		io.WriteString(responseWriter, `{"nodes":[],"edges":[],"count":0}`)
	}))
	defer server.Close()

	mem := NewMemory("k", WithURL(server.URL))
	if _, walkErr := mem.WalkSemantic(context.Background(), startID, depth, opts...); walkErr != nil {
		t.Fatalf("WalkSemantic returned error: %v", walkErr)
	}
	return capturedBody
}

// TestWalkSemanticBodySerialization is a table-driven proof that each goal
// option lands in the request body under the exact key the locked REST contract
// expects, and that an option-free call stays backward compatible.
func TestWalkSemanticBodySerialization(t *testing.T) {
	goalVector := []byte{0x01, 0x02, 0x03, 0xff}
	goalVectorB64 := base64.StdEncoding.EncodeToString(goalVector)

	testCases := []struct {
		name        string
		options     []ReadOption
		wantPresent map[string]interface{} // key -> expected value
		wantAbsent  []string               // keys that must NOT be in the body
	}{
		{
			name:    "no options keeps legacy Dijkstra body",
			options: nil,
			wantPresent: map[string]interface{}{
				"seed_id": float64(7),
				"depth":   float64(3),
			},
			wantAbsent: []string{"target", "vector", "target_tag", "max_cost"},
		},
		{
			name: "semantic target encodes goal vector as base64",
			options: []ReadOption{
				WithTarget("semantic"),
				WithGoalVector(goalVector),
			},
			wantPresent: map[string]interface{}{
				"target": "semantic",
				"vector": goalVectorB64,
			},
			wantAbsent: []string{"target_tag", "max_cost"},
		},
		{
			name: "tag target sends target_tag",
			options: []ReadOption{
				WithTarget("tag"),
				WithTargetTag("acme-corp"),
			},
			wantPresent: map[string]interface{}{
				"target":     "tag",
				"target_tag": "acme-corp",
			},
			wantAbsent: []string{"vector", "max_cost"},
		},
		{
			name: "recency target with explicit max_cost",
			options: []ReadOption{
				WithTarget("recency"),
				WithMaxCost(3.5),
			},
			wantPresent: map[string]interface{}{
				"target":   "recency",
				"max_cost": float64(3.5),
			},
			wantAbsent: []string{"vector", "target_tag"},
		},
		{
			name: "non-positive max_cost is omitted (server default applies)",
			options: []ReadOption{
				WithMaxCost(0),
			},
			wantPresent: map[string]interface{}{
				"seed_id": float64(7),
			},
			wantAbsent: []string{"max_cost"},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			body := captureWalkBody(t, 7, 3, testCase.options...)

			for wantKey, wantValue := range testCase.wantPresent {
				gotValue, present := body[wantKey]
				if !present {
					t.Fatalf("body missing key %q; got %#v", wantKey, body)
				}
				if gotValue != wantValue {
					t.Fatalf("body[%q] = %#v, want %#v", wantKey, gotValue, wantValue)
				}
			}
			for _, absentKey := range testCase.wantAbsent {
				if _, present := body[absentKey]; present {
					t.Fatalf("body should not contain key %q; got %#v", absentKey, body)
				}
			}
		})
	}
}
