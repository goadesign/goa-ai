package prompt

import (
	"crypto/sha256"
	"encoding/hex"
)

// VersionFromTemplate derives a deterministic prompt version identifier from
// template source content.
func VersionFromTemplate(template string) string {
	sum := sha256.Sum256([]byte(template))
	return "sha256:" + hex.EncodeToString(sum[:])
}
