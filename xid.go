package synthigy

import "crypto/rand"

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// NewXID returns a fresh 22-char Base58 xid — client-minted identity for
// Sync/Stack, the same derivation the server uses (UUID bytes -> base58,
// left-padded with '1').
//
// Mint one before a write to know a record's id up front, or to make a retried
// write idempotent — the server accepts a caller-supplied id as-is, and the
// alternative (Returning()) costs the full echo on every write.
func NewXID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("synthigy: crypto/rand failed: " + err.Error())
	}
	// UUIDv4 bit layout, so the value round-trips the server's uuid->nanoid.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80

	// Schoolbook base-256 -> base-58, the same conversion Bitcoin-style base58
	// libraries use.
	digits := []int{0}
	for _, by := range b {
		carry := int(by)
		for i := range digits {
			x := digits[i]*256 + carry
			digits[i] = x % 58
			carry = x / 58
		}
		for carry > 0 {
			digits = append(digits, carry%58)
			carry /= 58
		}
	}

	out := make([]byte, 0, 22)
	for i := 22 - len(digits); i > 0; i-- {
		out = append(out, '1')
	}
	for i := len(digits) - 1; i >= 0; i-- {
		out = append(out, base58Alphabet[digits[i]])
	}
	return string(out)
}
