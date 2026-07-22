package tunnelidentity

import (
	"crypto/sha256"
	"encoding/hex"
)

// Tag returns the only Xray inbound tag used for a managed tunnel resource.
// Controllers and Agents must share this derivation so traffic attribution and
// lifecycle operations address the same resource without accepting raw tags.
func Tag(resourceID string) string {
	digest := sha256.Sum256([]byte("relaydock-managed-tunnel-v1\x00" + resourceID))
	return "rd-tun-" + hex.EncodeToString(digest[:16])
}
