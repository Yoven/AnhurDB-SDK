package client

// record_metadata_patch_test.go — Update puts metadata on the wire as the JSON
// STRING the server decodes, keeping nil values as null so a merge patch can
// delete keys. Parity: Python tests/test_record_metadata_patch.py, TypeScript
// src/test/record_metadata_patch.test.ts (same cases, same expectations).

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// patchBodyRecorder answers 200 to every request and keeps each raw body.
func patchBodyRecorder(t *testing.T) (*Memory, func() [][]byte) {
	t.Helper()
	var recordedMutex sync.Mutex
	var recordedBodies [][]byte
	stubServer := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, httpRequest *http.Request) {
		requestBody, readError := io.ReadAll(httpRequest.Body)
		if readError != nil {
			t.Errorf("read body: %v", readError)
		}
		recordedMutex.Lock()
		recordedBodies = append(recordedBodies, requestBody)
		recordedMutex.Unlock()
		responseWriter.Header().Set("Content-Type", "application/json")
		_, _ = responseWriter.Write([]byte(`{"message":"record updated","last_raft_index":1}`))
	}))
	t.Cleanup(stubServer.Close)
	return NewMemory("test-key", WithURL(stubServer.URL)), func() [][]byte {
		recordedMutex.Lock()
		defer recordedMutex.Unlock()
		return append([][]byte(nil), recordedBodies...)
	}
}

func TestUpdateMetadataWireShape(t *testing.T) {
	testCases := []struct {
		name             string
		metadataValue    interface{}
		wantMetadataJSON map[string]interface{} // decoded from the wire string; nil = expect the raw string below
		wantRawString    string
	}{
		{
			name:             "map keeps nil as null so the server deletes the key",
			metadataValue:    map[string]interface{}{"keep": "x", "drop_me": nil, "nested": map[string]interface{}{"inner": nil}},
			wantMetadataJSON: map[string]interface{}{"keep": "x", "drop_me": nil, "nested": map[string]interface{}{"inner": nil}},
		},
		{
			name:             "empty map is sent as the empty object (a no-op patch)",
			metadataValue:    map[string]interface{}{},
			wantMetadataJSON: map[string]interface{}{},
		},
		{
			name:          "string metadata passes through byte-for-byte (backwards compatible)",
			metadataValue: `{"a":1,"b":null}`,
			wantRawString: `{"a":1,"b":null}`,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			memory, recordedBodies := patchBodyRecorder(t)
			callerUpdates := map[string]interface{}{"summary": "s", "metadata": testCase.metadataValue}
			if updateError := memory.Update(context.Background(), 42, callerUpdates); updateError != nil {
				t.Fatalf("Update: %v", updateError)
			}
			bodies := recordedBodies()
			if len(bodies) != 1 {
				t.Fatalf("requests=%d want 1", len(bodies))
			}
			var wireBody map[string]interface{}
			if decodeError := json.Unmarshal(bodies[0], &wireBody); decodeError != nil {
				t.Fatalf("decode body %q: %v", bodies[0], decodeError)
			}
			wireMetadata, isString := wireBody["metadata"].(string)
			if !isString {
				t.Fatalf("metadata on the wire is %T (%v), want a JSON string — the server field is a string", wireBody["metadata"], wireBody["metadata"])
			}
			if wireBody["summary"] != "s" {
				t.Fatalf("summary=%v want s (other fields must travel unchanged)", wireBody["summary"])
			}
			if testCase.wantMetadataJSON == nil {
				if wireMetadata != testCase.wantRawString {
					t.Fatalf("metadata=%q want %q", wireMetadata, testCase.wantRawString)
				}
				return
			}
			var decodedPatch map[string]interface{}
			if decodeError := json.Unmarshal([]byte(wireMetadata), &decodedPatch); decodeError != nil {
				t.Fatalf("metadata string %q is not JSON: %v", wireMetadata, decodeError)
			}
			wantEncoded, _ := json.Marshal(testCase.wantMetadataJSON)
			gotEncoded, _ := json.Marshal(decodedPatch)
			if string(gotEncoded) != string(wantEncoded) {
				t.Fatalf("metadata patch=%s want %s", gotEncoded, wantEncoded)
			}
			if _, stillMap := callerUpdates["metadata"].(map[string]interface{}); testCase.wantRawString == "" && !stillMap {
				t.Fatalf("Update mutated the caller's map: metadata is now %T", callerUpdates["metadata"])
			}
		})
	}
}

func TestUpdateMetadataUnencodableIsRefusedBeforeTheWire(t *testing.T) {
	memory, recordedBodies := patchBodyRecorder(t)
	updateError := memory.Update(context.Background(), 42, map[string]interface{}{
		"metadata": map[string]interface{}{"ratio": math.NaN()},
	})
	if updateError == nil {
		t.Fatal("NaN in a metadata map must be refused (JSON has no NaN)")
	}
	var apiError *APIError
	if !errors.As(updateError, &apiError) || apiError.Kind() != KindInvalidRequest {
		t.Fatalf("error=%v want *APIError with KindInvalidRequest", updateError)
	}
	if len(recordedBodies()) != 0 {
		t.Fatal("a refused metadata map must not reach the wire")
	}
}
