package main

import (
	"crypto/rand"

	"golang.org/x/crypto/argon2"
)

type HashAndSalt struct {
	Hash []byte
	Salt []byte
}

func HashAndSaltPassword(password []byte) (*HashAndSalt, error) {
	salt := make([]byte, 32)
	_, err := rand.Read(salt)
	if err != nil {
		return nil, err
	}
	// Parameters from:
	// https://pkg.go.dev/golang.org/x/crypto/argon2#pkg-overview
	hash := argon2.IDKey(password, salt, 1, 64*1024, 4, 32)
	return &HashAndSalt{Hash: hash, Salt: salt}, err
}
