// Package id generates URL-safe, sortable identifiers without external deps.
package id

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// New returns a k-sortable ID: 8 hex chars of unix seconds + 16 random hex chars.
// Lexicographic order approximates creation order, which keeps SQLite B-tree
// inserts near-append-only and makes debugging dumps readable.
func New(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("id: entropy source failed: %v", err))
	}
	return fmt.Sprintf("%s_%08x%s", prefix, uint32(time.Now().Unix()), hex.EncodeToString(b[:]))
}
