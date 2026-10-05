package session

import (
	"os"

	"github.com/creack/pty"
)

func ptyPair() (*os.File, *os.File, error) { return pty.Open() }
