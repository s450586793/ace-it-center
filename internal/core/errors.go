package core

import "errors"

var (
	ErrNotFound          = errors.New("not found")
	ErrConflict          = errors.New("conflict")
	ErrUnauthorized      = errors.New("unauthorized")
	ErrEnrollmentExpired = errors.New("enrollment expired")
	ErrPairingExpired    = errors.New("pairing expired")
	ErrRateLimited       = errors.New("rate limited")
)
