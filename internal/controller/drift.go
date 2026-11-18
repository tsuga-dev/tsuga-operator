package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
)

const fieldHashLength = 16

func payloadKeys(payload []byte) ([]string, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(payload, &obj); err != nil {
		return nil, fmt.Errorf("decode payload: %w", err)
	}
	return slices.Sorted(maps.Keys(obj)), nil
}

func fieldHashes(remote []byte, keys []string) (map[string]string, error) {
	var obj map[string]any
	if err := json.Unmarshal(remote, &obj); err != nil {
		return nil, fmt.Errorf("decode remote: %w", err)
	}
	hashes := make(map[string]string, len(keys))
	for _, k := range keys {
		canonical, err := json.Marshal(obj[k])
		if err != nil {
			return nil, fmt.Errorf("encode field %q: %w", k, err)
		}
		sum := sha256.Sum256(canonical)
		hashes[k] = hex.EncodeToString(sum[:])[:fieldHashLength]
	}
	return hashes, nil
}

func driftedFields(baseline, current map[string]string) []string {
	drifted := make([]string, 0, len(baseline))
	for k, h := range baseline {
		if cur, ok := current[k]; !ok || cur != h {
			drifted = append(drifted, k)
		}
	}
	slices.Sort(drifted)
	return drifted
}

// holdsDesired reports whether remote carries every value desired sets. Keys
// only remote has are ignored: Tsuga adds ids the payload never sends, such as
// an SLO alert's id and sloId.
func holdsDesired(remote, desired any) bool {
	switch d := desired.(type) {
	case map[string]any:
		r, ok := remote.(map[string]any)
		if !ok {
			return false
		}
		for k, v := range d {
			if !holdsDesired(r[k], v) {
				return false
			}
		}
		return true
	case []any:
		r, ok := remote.([]any)
		if !ok || len(r) != len(d) {
			return false
		}
		for i := range d {
			if !holdsDesired(r[i], d[i]) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(remote, desired)
	}
}
