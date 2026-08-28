package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/martinstenrose/wordleland/internal/auth"
	"golang.org/x/term"
)

// minPasswordLength is defined in internal/auth so the CLI and the web reset
// form agree on it.
const minPasswordLength = auth.MinPasswordLength

// readNewPassword prompts twice and returns the password.
//
// It reads from the terminal with echo disabled rather than taking a flag: a
// --password flag would put the password in shell history, in the process
// list, and in any transcript of the session.
func readNewPassword(e *env) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		// Not a terminal: accept a single line from stdin so the command can
		// be scripted, but only that. There is still no flag.
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", errors.New("no password on stdin, and stdin is not a terminal to prompt on")
		}
		password := strings.TrimRight(line, "\r\n")
		return password, validatePassword(password)
	}

	fmt.Fprint(e.out, "New password: ")
	first, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(e.out)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}

	fmt.Fprint(e.out, "Repeat password: ")
	second, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(e.out)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}

	if string(first) != string(second) {
		return "", errors.New("the two passwords do not match")
	}
	password := string(first)
	return password, validatePassword(password)
}

func validatePassword(password string) error {
	if len([]rune(password)) < minPasswordLength {
		return fmt.Errorf("password must be at least %d characters", minPasswordLength)
	}
	return nil
}
