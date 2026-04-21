package util

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

func CanonicalHash(v any) (string, []byte, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), body, nil
}
