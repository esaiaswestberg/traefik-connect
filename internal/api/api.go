package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	HeaderTimestamp = "X-Traefik-Connect-Timestamp"
	HeaderSignature = "X-Traefik-Connect-Signature"
	HeaderAgent     = "X-Traefik-Connect-Agent"
)

func SignBody(token string, ts time.Time, body []byte) string {
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = mac.Write([]byte(ts.UTC().Format(time.RFC3339Nano)))
	_, _ = mac.Write([]byte{'\n'})
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func ValidateRequest(r *http.Request, token string, window time.Duration, maxBytes int64) ([]byte, time.Time, error) {
	if token == "" {
		return nil, time.Time{}, errors.New("server token not configured")
	}
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") || strings.TrimSpace(strings.TrimPrefix(auth, "Bearer ")) != token {
		return nil, time.Time{}, errors.New("unauthorized")
	}

	rawTs := strings.TrimSpace(r.Header.Get(HeaderTimestamp))
	if rawTs == "" {
		return nil, time.Time{}, errors.New("missing timestamp")
	}
	ts, err := time.Parse(time.RFC3339Nano, rawTs)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("invalid timestamp: %w", err)
	}
	if window > 0 {
		now := time.Now().UTC()
		if ts.Before(now.Add(-window)) || ts.After(now.Add(window)) {
			return nil, time.Time{}, fmt.Errorf("timestamp outside allowed window")
		}
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxBytes+1))
	if err != nil {
		return nil, time.Time{}, err
	}
	if int64(len(body)) > maxBytes {
		return nil, time.Time{}, fmt.Errorf("request body too large")
	}
	sig := strings.TrimSpace(r.Header.Get(HeaderSignature))
	if sig == "" {
		return nil, time.Time{}, errors.New("missing signature")
	}
	expected := SignBody(token, ts, body)
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return nil, time.Time{}, errors.New("invalid signature")
	}
	return body, ts, nil
}
