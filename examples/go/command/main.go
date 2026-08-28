package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/hatchet-dev/hatchet/pkg/cmdutils"
	hatchet "github.com/hatchet-dev/hatchet/sdks/go"

	"github.com/hatchet-dev/brickie/sdks/go"
)

type SessionOutput struct {
	T1 sandbox.TurnResult `json:"t1"`
	T2 sandbox.TurnResult `json:"t2"`
	A  sandbox.TurnResult `json:"a"`
	B  sandbox.TurnResult `json:"b"`
}

func main() {
	client, err := hatchet.NewClient()
	if err != nil {
		log.Fatal(err)
	}
	sbx := sandbox.New(client, sandbox.Options{})

	session := client.NewStandaloneDurableTask("command-session-go", func(ctx hatchet.DurableContext, _ struct{}) (SessionOutput, error) {
		t1, err := sbx.ExecuteCommand(ctx, "echo one > log.txt && uname -s")
		if err != nil {
			return SessionOutput{}, err
		}
		t2, err := sbx.ExecuteCommand(ctx, "echo two >> log.txt && wc -l < log.txt", sandbox.ExecOptions{From: t1.SnapshotID})
		if err != nil {
			return SessionOutput{}, err
		}
		forks, err := sbx.ExecuteAll(ctx, []sandbox.Item{
			{Command: "echo a >> log.txt && tr '\\n' ' ' < log.txt", From: t2.SnapshotID},
			{Command: "echo b >> log.txt && tr '\\n' ' ' < log.txt", From: t2.SnapshotID},
		})
		if err != nil {
			return SessionOutput{}, err
		}
		return SessionOutput{T1: *t1, T2: *t2, A: forks[0], B: forks[1]}, nil
	})

	worker, err := client.NewWorker("command-worker-go", hatchet.WithWorkflows(session), hatchet.WithDurableSlots(10))
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := cmdutils.NewInterruptContext()
	defer cancel()
	go func() {
		if err := worker.StartBlocking(ctx); err != nil {
			log.Fatal(err)
		}
	}()

	res, err := client.Run(context.Background(), "command-session-go", struct{}{})
	if err != nil {
		log.Fatal(err)
	}
	rawRes, _ := json.Marshal(res.Raw())
	var byTask map[string]json.RawMessage
	_ = json.Unmarshal(rawRes, &byTask)
	if inner, ok := byTask["command-session-go"]; ok {
		rawRes = inner
	}
	var out SessionOutput
	if err := json.Unmarshal(rawRes, &out); err != nil {
		log.Fatal(err)
	}
	pretty, _ := json.MarshalIndent(out, "", "  ")
	fmt.Printf("session result: %s\n", pretty)

	a, _ := out.A.Command()
	b, _ := out.B.Command()
	if strings.TrimSpace(a.Stdout) != "one two a" || strings.TrimSpace(b.Stdout) != "one two b" {
		fmt.Println("e2e FAILED")
		os.Exit(1)
	}
	fmt.Println("e2e ok")
	cancel()
}
