//go:build ignore

package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

var conventionalSubject = regexp.MustCompile(`^(build|chore|ci|docs|feat|fix|perf|refactor|revert|style|test)(\([a-z0-9][a-z0-9._/-]*\))?(!)?: .+`)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go run check-commit-message.go <message-file>")
		os.Exit(2)
	}

	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "read commit message: %v\n", err)
		os.Exit(2)
	}

	subject := strings.SplitN(string(data), "\n", 2)[0]
	subject = strings.TrimSuffix(subject, "\r")
	if strings.HasPrefix(subject, "Merge ") || strings.HasPrefix(subject, "Revert ") {
		return // Preserve Git-generated merge and revert subjects.
	}
	if !conventionalSubject.MatchString(subject) {
		fmt.Fprintln(os.Stderr, "commit subject must be Conventional Commits: type(scope)!: description")
		os.Exit(1)
	}
}
