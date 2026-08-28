package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"sigs.k8s.io/agent-sandbox/clients/go/sandbox"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: brickie <command...>")
		os.Exit(2)
	}
	ctx := context.Background()

	client, err := sandbox.NewClient(ctx, sandbox.Options{Namespace: "default", Quiet: true})
	if err != nil {
		log.Fatal(err)
	}
	defer client.DeleteAll(ctx)

	sb, err := client.CreateSandbox(ctx, "brickie-pool", "default")
	if err != nil {
		log.Fatal(err)
	}

	command := strings.Join(os.Args[1:], " ")
	result, err := sb.Run(ctx, "sh -c '"+strings.ReplaceAll(command, "'", `'\''`)+"'")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Print(result.Stdout)
	fmt.Fprint(os.Stderr, result.Stderr)
	client.DeleteAll(ctx)
	os.Exit(result.ExitCode)
}
