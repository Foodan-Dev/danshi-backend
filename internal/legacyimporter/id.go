package legacyimporter

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math"
	"strings"

	"github.com/google/uuid"
)

func mapUUID(raw string) (int64, error) {
	parsed, err := uuid.Parse(raw)
	if err != nil {
		return 0, errors.New("invalid uuid")
	}
	return mappedID("uuid", parsed.String()), nil
}

func mapText(namespace, value string) int64 {
	return mappedID(namespace, value)
}

func mappedID(namespace, value string) int64 {
	digest := sha256.Sum256([]byte("danshi-legacy-import/v1\x00" + namespace + "\x00" + value))
	id := binary.BigEndian.Uint64(digest[:8]) & math.MaxInt64
	if id == 0 {
		return 1
	}
	return int64(id)
}

func relationKey(values ...string) string {
	return strings.Join(values, ":")
}
