package iprangedb

import "fmt"

type Error struct {
	msg string
}

func (e *Error) Error() string { return e.msg }

func errf(category, msg string) error {
	return &Error{msg: fmt.Sprintf("%s: %s", category, msg)}
}
