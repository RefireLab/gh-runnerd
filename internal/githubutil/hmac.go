package githubutil

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// SignBody returns the GitHub X-Hub-Signature-256 value for body.
func SignBody(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// ValidSignature checks X-Hub-Signature-256 using a constant-time compare.
func ValidSignature(secret, header string, body []byte) bool {
	if secret == "" {
		return false
	}
	want := SignBody(secret, body)
	return hmac.Equal([]byte(strings.ToLower(header)), []byte(strings.ToLower(want)))
}

// SignatureError is returned when a webhook signature is missing or wrong.
type SignatureError struct {
	Reason string
}

func (e SignatureError) Error() string {
	return fmt.Sprintf("webhook signature: %s", e.Reason)
}
