package books

import (
	"fmt"
	"strings"
)

// minPrefix is the shortest fingerprint prefix a command accepts. Four characters is git's floor
// for the same reason: short enough to type off a listing, long enough that a stray argument
// cannot land on an arbitrary record.
const minPrefix = 4

// byPrefix finds the one record whose id is exactly key or uniquely begins with it, so a
// fingerprint is quoted the way a git hash is: enough of the front to be unique. An exact match
// always wins, which is what keeps "abc123" and "abc123-1" (the second sighting of an identical
// line) both reachable. An ambiguous prefix is refused by name, listing what it could have meant,
// rather than resolved by guessing.
func byPrefix[T any](items []T, key string, id func(T) string) (T, bool, error) {
	var zero T
	var matched []T
	for _, item := range items {
		if id(item) == key {
			return item, true, nil
		}
		if len(key) >= minPrefix && strings.HasPrefix(id(item), key) {
			matched = append(matched, item)
		}
	}
	switch len(matched) {
	case 0:
		return zero, false, nil
	case 1:
		return matched[0], true, nil
	default:
		ids := make([]string, len(matched))
		for i, m := range matched {
			ids[i] = id(m)
		}
		return zero, false, fmt.Errorf("books: %q is ambiguous: it could be %s", key, strings.Join(ids, ", "))
	}
}
