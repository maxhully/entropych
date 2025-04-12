package main

import "golang.org/x/crypto/argon2"

func HashPassword(password []byte, salt []byte) []byte {
	// Parameters from:
	// https://pkg.go.dev/golang.org/x/crypto/argon2#pkg-overview
	return argon2.IDKey(password, salt, 1, 64*1024, 4, 32)
}
