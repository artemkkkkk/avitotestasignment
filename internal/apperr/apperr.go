package apperr

import "net/http"

type Error struct {
	Status  int
	Code    string
	Message string
	Details any
}

func (e Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return e.Code
}

func New(status int, code, message string) Error {
	return Error{
		Status:  status,
		Code:    code,
		Message: message,
	}
}

func WithDetails(e Error, details any) Error {
	e.Details = details
	return e
}

func BadRequest(message string) Error {
	return New(http.StatusBadRequest, "BAD_REQUEST", message)
}

func Unauthorized(message string) Error {
	return New(http.StatusUnauthorized, "UNAUTHORIZED", message)
}

func NotFound(message string) Error {
	return New(http.StatusNotFound, "NOT_FOUND", message)
}

func Conflict(message string) Error {
	return New(http.StatusConflict, "CONFLICT", message)
}

func Internal(message string) Error {
	return New(http.StatusInternalServerError, "INTERNAL_ERROR", message)
}
