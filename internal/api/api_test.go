package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestValidateRequest(t *testing.T) {
	ts := time.Now().UTC().Truncate(time.Second)
	body := []byte(`{"worker_id":"worker-a","captured_at":"` + ts.Format(time.RFC3339Nano) + `","version":"v1","hash":"abc","containers":[]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/snapshot", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set(HeaderTimestamp, ts.Format(time.RFC3339Nano))
	req.Header.Set(HeaderSignature, SignBody("secret", ts, body))
	got, _, err := ValidateRequest(req, "secret", 5*time.Minute, 1024)
	if err != nil {
		t.Fatalf("ValidateRequest() error = %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("body mismatch")
	}
}

func TestValidateRequestRejectsBadSignature(t *testing.T) {
	body := []byte(`{"worker_id":"worker-a"}`)
	ts := time.Now().UTC()
	req := httptest.NewRequest(http.MethodPost, "/v1/snapshot", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set(HeaderTimestamp, ts.Format(time.RFC3339Nano))
	req.Header.Set(HeaderSignature, "bad")
	_, _, err := ValidateRequest(req, "secret", 5*time.Minute, 1024)
	if err == nil {
		t.Fatal("expected error")
	}
}
