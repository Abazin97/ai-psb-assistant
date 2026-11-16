package repository

import "errors"

var (
	ErrNotFound     = errors.New("not found")
	ErrMetaTooLarge = errors.New("metadata exceeds 64 KiB")
)
