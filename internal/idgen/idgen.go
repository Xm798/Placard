// Package idgen produces unpredictable share identifiers.
//
// IDs are generated with crypto/rand (math/rand is forbidden): an 8-char nanoid
// over [A-Za-z0-9] gives 62^8 ≈ 2.18e14 of keyspace, short-link friendly and
// not guessable. Insert collisions on uk_nano_id are retried (<=3) by callers.
package idgen

import (
	"crypto/rand"
	"math/big"
)

const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// Generate returns an n-character random ID over [A-Za-z0-9] using crypto/rand.
// It panics only if the system CSPRNG fails, which is unrecoverable.
func Generate(n int) string {
	if n <= 0 {
		return ""
	}
	max := big.NewInt(int64(len(alphabet)))
	b := make([]byte, n)
	for i := 0; i < n; i++ {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			panic("idgen: crypto/rand failure: " + err.Error())
		}
		b[i] = alphabet[idx.Int64()]
	}
	return string(b)
}
