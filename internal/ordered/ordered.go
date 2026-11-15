// Package ordered provides small insertion-ordered maps.
package ordered

// Map is a map that remembers the order in which keys were first inserted.
// It is not safe for concurrent use.
type Map[K comparable, V any] struct {
	keys []K
	vals map[K]V
}

// New returns an empty ordered map.
func New[K comparable, V any]() *Map[K, V] {
	return &Map[K, V]{vals: make(map[K]V)}
}

// Set inserts or replaces a value, appending the key on first insertion.
func (m *Map[K, V]) Set(key K, value V) {
	if m.vals == nil {
		m.vals = make(map[K]V)
	}
	if _, ok := m.vals[key]; !ok {
		m.keys = append(m.keys, key)
	}
	m.vals[key] = value
}

// Get returns the value stored for key.
func (m *Map[K, V]) Get(key K) (V, bool) {
	if m == nil || m.vals == nil {
		var zero V
		return zero, false
	}
	value, ok := m.vals[key]
	return value, ok
}

// Delete removes key, preserving the order of the remaining keys.
func (m *Map[K, V]) Delete(key K) {
	if m == nil || m.vals == nil {
		return
	}
	if _, ok := m.vals[key]; !ok {
		return
	}
	delete(m.vals, key)
	for i, existing := range m.keys {
		if existing == key {
			m.keys = append(m.keys[:i], m.keys[i+1:]...)
			break
		}
	}
}

// Keys returns the keys in insertion order.
func (m *Map[K, V]) Keys() []K {
	if m == nil {
		return nil
	}
	return append([]K(nil), m.keys...)
}

// Len returns the number of entries.
func (m *Map[K, V]) Len() int {
	if m == nil {
		return 0
	}
	return len(m.keys)
}

// Range calls fn for every entry in insertion order. Returning false stops.
func (m *Map[K, V]) Range(fn func(key K, value V) bool) {
	if m == nil {
		return
	}
	for _, key := range m.keys {
		if !fn(key, m.vals[key]) {
			return
		}
	}
}
