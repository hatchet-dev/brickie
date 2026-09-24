package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/charmbracelet/huh/spinner"
	"golang.org/x/term"
)

func interactive() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

func step(ctx context.Context, title string, fn func(ctx context.Context) error) error {
	if !interactive() {
		say("%s", title)
		return fn(ctx)
	}
	var fnErr error
	spinErr := spinner.New().Title(title).ActionWithErr(func(sctx context.Context) error {
		fnErr = fn(sctx)
		return fnErr
	}).Run()
	if fnErr != nil {
		return fnErr
	}
	return spinErr
}

func say(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "\n==> "+format+"\n", args...)
}

func have(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func output(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

func runStdin(stdin, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
