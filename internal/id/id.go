package id

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// New returns a short, opaque identifier suitable for public API resources.
// The timestamp makes identifiers easy to sort while the random suffix avoids
// collisions when several requests are created in the same nanosecond.
func New(prefix string) string {
	buf := make([]byte, 5)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return fmt.Sprintf("%s_%d_%s", prefix, time.Now().UnixMilli(), hex.EncodeToString(buf))
}
