// Package api holds the OpenAPI description of the HTTP interface and
// exports its SHA-256 digest so the server can report it at runtime.
package api

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

//go:embed openapi.yaml
var schema []byte

// Sha256 returns the lower-case hex SHA-256 of the embedded openapi.yaml.
func Sha256() string {
	sum := sha256.Sum256(schema)
	return hex.EncodeToString(sum[:])
}
