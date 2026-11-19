package api

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

//go:embed openapi.yaml
var schema []byte

func Sha256() string {
	sum := sha256.Sum256(schema)
	return hex.EncodeToString(sum[:])
}
