package client

import "errors"

var (
	ErrUnauthorized   = errors.New("authentication failed: invalid API key")
	ErrInvalidQuery   = errors.New("invalid query request")
	ErrConnectionFail = errors.New("failed to connect to AnhurDB server")
	ErrServerError    = errors.New("internal server error")
)
