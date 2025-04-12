package main

import (
	"crypto/rand"
	"crypto/subtle"

	"golang.org/x/crypto/argon2"
)

type HashAndSalt struct {
	Hash []byte
	Salt []byte
}

func hashPassword(password []byte, salt []byte) []byte {
	// Parameters from:
	// https://pkg.go.dev/golang.org/x/crypto/argon2#pkg-overview
	return argon2.IDKey(password, salt, 1, 64*1024, 4, 32)
}

func HashAndSaltPassword(password []byte) (*HashAndSalt, error) {
	salt := make([]byte, 32)
	_, err := rand.Read(salt)
	if err != nil {
		return nil, err
	}
	hash := hashPassword(password, salt)
	return &HashAndSalt{Hash: hash, Salt: salt}, err
}

func CheckPassword(password []byte, hashAndSalt HashAndSalt) bool {
	hash := hashPassword(password, hashAndSalt.Salt)
	if len(hash) != len(hashAndSalt.Hash) {
		return false
	}
	return subtle.ConstantTimeCompare(hash, hashAndSalt.Hash) == 1
}
