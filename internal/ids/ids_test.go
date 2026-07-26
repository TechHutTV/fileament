package ids

import (
	"strings"
	"testing"
)

func TestNewReturnsLexicallyOrderedCrockfordULIDs(t *testing.T) {
	const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	previous := ""
	for range 100 {
		id := New()
		if len(id) != 26 {
			t.Fatalf("ULID length = %d, want 26", len(id))
		}
		for _, r := range id {
			if !strings.ContainsRune(crockford, r) {
				t.Fatalf("ULID %q contains non-Crockford character %q", id, r)
			}
		}
		if previous != "" && id <= previous {
			t.Fatalf("ULIDs are not monotonic: %q then %q", previous, id)
		}
		previous = id
	}
}
