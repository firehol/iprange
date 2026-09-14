package handlers

import (
	"errors"
	"os"
)

// errOpenedNotRegular reports that the descriptor one caller-input open
// actually returned is not a regular file (Rust io::caller_open::
// open_regular NotRegular). Every user-path open in these handlers
// returns it, and each arm maps it to its own refusal so a pre-placed
// node and a node swapped in at the open instant answer with the same
// class and message.
var errOpenedNotRegular = errors.New("opened descriptor is not a regular file")

// checkOpenedRegular judges the opened descriptor, the only identity a
// caller may trust after its path check (Rust retained_regular_identity
// over the fd the open returned).
func checkOpenedRegular(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errOpenedNotRegular
	}
	return nil
}
