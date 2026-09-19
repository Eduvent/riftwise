package recommendation

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

var fallbackEpochCounter atomic.Uint64

// NewEpoch returns an opaque identifier for one backend process/coordinator
// instance. Randomness prevents browser revision counters from being confused
// across restarts; the time/counter fallback remains unique within a process if
// the operating system random source is unavailable.
func NewEpoch() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err == nil {
		return hex.EncodeToString(value[:])
	}
	return fmt.Sprintf("fallback-%x-%x", time.Now().UTC().UnixNano(), fallbackEpochCounter.Add(1))
}

// ValidEpoch bounds the value before it is used in JSON and SSE event IDs.
func ValidEpoch(value string) bool {
	trimmed := strings.TrimSpace(value)
	if value != trimmed || len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}
