package client_test

import (
	"testing"
	"github.com/yoven/anhurdb-sdk/v2/golang/v2/client"
)

func TestNewConnection(t *testing.T) {
	conn := client.NewConnection("http://localhost:8080/", "test_key")
	
	if conn.BaseURL != "http://localhost:8080" {
		t.Errorf("Expected base url http://localhost:8080, got %s", conn.BaseURL)
	}
	
	if conn.APIKey != "test_key" {
		t.Errorf("Expected APIKey test_key, got %s", conn.APIKey)
	}
	
	if conn.HTTPClient == nil {
		t.Error("Expected HTTPClient to be initialized")
	}
}
