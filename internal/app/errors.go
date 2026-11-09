package app

import "fmt"

type ExitError struct {
	Code    int
	Message string
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("%s (exit code %d)", e.Message, e.Code)
}
