package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

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

func bootstrapLocalCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "bootstrap-local",
		Short: "Install and configure Docker Sandboxes for local sandbox turns",
		RunE: func(cmd *cobra.Command, args []string) error {
			say("Checking Docker Desktop")
			if !have("docker") {
				return errors.New("install Docker Desktop first: https://www.docker.com/products/docker-desktop/")
			}
			if _, err := output("docker", "info"); err != nil {
				return errors.New("start Docker Desktop, then re-run")
			}

			say("Installing sbx (Docker Sandboxes CLI)")
			if have("sbx") {
				v, _ := output("sbx", "version")
				fmt.Print(v)
			} else {
				if !have("brew") {
					return errors.New("Homebrew is required to install sbx: https://brew.sh")
				}
				if err := run("brew", "trust", "docker/tap"); err != nil {
					return err
				}
				if err := run("brew", "install", "docker/tap/sbx"); err != nil {
					return err
				}
			}

			say("Docker sign-in")
			if _, err := output("sbx", "ls"); err != nil {
				if err := run("sbx", "login"); err != nil {
					return err
				}
			} else {
				fmt.Println("already signed in")
			}

			say("Global sandbox network policy")
			if out, _ := output("sbx", "policy", "ls"); strings.Contains(out, "local-policy") {
				fmt.Println("already initialized")
			} else if err := run("sbx", "policy", "init", "balanced"); err != nil {
				return err
			}

			say("Claude auth inside sandboxes (opens a browser)")
			if err := run("sbx", "run", "claude", "--", "auth", "login"); err != nil {
				return err
			}

			say("Done. Local sandbox turns will run in Docker Sandboxes.")
			return nil
		},
	}
}
