package engine

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hatchet-dev/hatchet/pkg/client/operatorclient"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/hatchet-dev/brickie/internal/injector"
)

const (
	actionTypeStartStepRun  = 0
	actionTypeCancelStepRun = 1

	stepEventStarted   = "STEP_EVENT_TYPE_STARTED"
	stepEventCompleted = "STEP_EVENT_TYPE_COMPLETED"
	stepEventFailed    = "STEP_EVENT_TYPE_FAILED"
	stepEventCancelled = "STEP_EVENT_TYPE_CANCELLED"
)

type turnAction struct {
	jobID      string
	jobRunID   string
	stepID     string
	stepRunID  string
	actionID   string
	payload    string
	retryCount int32
}

// newArgFor materializes the message type a session method takes, so the
// internal proto type is named only through inference and filled via
// protojson.
func newArgFor[E proto.Message, R any](func(context.Context, E) (R, error)) E {
	var zero E
	return zero.ProtoReflect().New().Interface().(E)
}

func newArgFor2[E proto.Message, R1, R2 any](func(context.Context, E) (R1, R2, error)) E {
	var zero E
	return zero.ProtoReflect().New().Interface().(E)
}

func operatorActionID() string {
	return strings.ToLower(TaskName + ":" + TaskName)
}

func dialHatchet(token string) (*grpc.ClientConn, error) {
	hostPort := os.Getenv("HATCHET_CLIENT_HOST_PORT")
	if hostPort == "" {
		var err error
		hostPort, _, err = injector.HatchetUpstreams(token)
		if err != nil {
			return nil, fmt.Errorf("resolve hatchet grpc address: %w", err)
		}
	}
	creds := credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
	if os.Getenv("HATCHET_CLIENT_TLS_STRATEGY") == "none" {
		creds = insecure.NewCredentials()
	}
	conn, err := grpc.NewClient(hostPort, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("dial hatchet at %s: %w", hostPort, err)
	}
	return conn, nil
}

type operatorProvider struct {
	runner   *turnRunner
	session  operatorclient.Session
	workerID string
	mu       sync.Mutex
	wg       sync.WaitGroup
	inFlight map[string]context.CancelFunc
}

func registerWorkflow(ctx context.Context, session operatorclient.Session, runner *turnRunner) ([]string, error) {
	request := newArgFor2(session.PutWorkflow)
	spec, err := json.Marshal(map[string]any{
		"name": TaskName,
		"tasks": []map[string]any{{
			"readableId": TaskName,
			"action":     operatorActionID(),
			"timeout":    runner.timeout.String(),
			"retries":    runner.retries,
		}},
	})
	if err != nil {
		return nil, err
	}
	if err := protojson.Unmarshal(spec, request); err != nil {
		return nil, fmt.Errorf("build workflow request: %w", err)
	}
	_, actions, err := session.PutWorkflow(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("register workflow: %w", err)
	}
	return actions, nil
}

func RunOperatorProvider(ctx context.Context, opts ProviderOpts, slots int) error {
	runner := newTurnRunner(opts)
	token := os.Getenv("HATCHET_CLIENT_TOKEN")
	if token == "" {
		return fmt.Errorf("HATCHET_CLIENT_TOKEN is required for operator mode")
	}
	conn, err := dialHatchet(token)
	if err != nil {
		return err
	}
	defer func() {
		_ = conn.Close()
	}()
	operator, err := operatorclient.New(conn, operatorclient.WithToken(token))
	if err != nil {
		return fmt.Errorf("build operator client: %w", err)
	}
	session, err := operator.Connect(ctx, &operatorclient.ConnectRequest{
		Name:       "brickie-provider",
		SlotConfig: map[string]int32{"default": int32(slots)},
	})
	if err != nil {
		return fmt.Errorf("connect operator (SERVER_GRPC_OPERATORS_ENABLED must be set for the tenant): %w", err)
	}
	defer func() {
		_ = session.Close()
	}()
	actions, err := registerWorkflow(ctx, session, runner)
	if err != nil {
		return err
	}
	session.AddActions(actions...)
	if err := session.Flush(ctx); err != nil {
		return fmt.Errorf("flush action set: %w", err)
	}
	assigned, errCh, err := session.Actions(ctx)
	if err != nil {
		return err
	}
	provider := &operatorProvider{
		runner:   runner,
		session:  session,
		workerID: session.Registration().WorkerId,
		inFlight: map[string]context.CancelFunc{},
	}
	fmt.Fprintf(os.Stderr, "brickie: sandbox provider connected as grpc operator (worker %s)\n", provider.workerID)
	for {
		select {
		case action, ok := <-assigned:
			if !ok {
				provider.wg.Wait()
				return fmt.Errorf("operator action stream closed")
			}
			a := turnAction{
				jobID:      action.JobId,
				jobRunID:   action.JobRunId,
				stepID:     action.TaskId,
				stepRunID:  action.TaskRunExternalId,
				actionID:   action.ActionId,
				payload:    action.ActionPayload,
				retryCount: action.RetryCount,
			}
			switch int32(action.ActionType) {
			case actionTypeStartStepRun:
				provider.wg.Add(1)
				go provider.execute(ctx, a)
			case actionTypeCancelStepRun:
				provider.cancel(a.stepRunID)
			}
		case streamErr, ok := <-errCh:
			if ok && streamErr != nil {
				fmt.Fprintf(os.Stderr, "brickie: operator stream error: %v\n", streamErr)
			}
		case <-ctx.Done():
			provider.wg.Wait()
			return ctx.Err()
		}
	}
}

func (p *operatorProvider) execute(ctx context.Context, a turnAction) {
	defer p.wg.Done()
	tctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.runner.timeout)
	p.mu.Lock()
	p.inFlight[a.stepRunID] = cancel
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.inFlight, a.stepRunID)
		p.mu.Unlock()
		cancel()
	}()

	p.sendEvent(a, stepEventStarted, "", nil)
	var payload struct {
		Input TurnInput `json:"input"`
	}
	if err := json.Unmarshal([]byte(a.payload), &payload); err != nil {
		noRetry := true
		p.sendEvent(a, stepEventFailed, fmt.Sprintf("decode turn input: %v", err), &noRetry)
		return
	}
	log := func(msg string) {
		fmt.Fprintf(os.Stderr, "brickie: [%s] %s\n", a.stepRunID, msg)
	}
	result, err := p.runner.run(tctx, a.stepRunID, payload.Input, log)
	if err != nil {
		if tctx.Err() != nil && ctx.Err() == nil {
			p.sendEvent(a, stepEventCancelled, "cancelled", nil)
			return
		}
		p.sendEvent(a, stepEventFailed, err.Error(), nil)
		return
	}
	out, err := json.Marshal(result)
	if err != nil {
		p.sendEvent(a, stepEventFailed, fmt.Sprintf("encode turn result: %v", err), nil)
		return
	}
	p.sendEvent(a, stepEventCompleted, string(out), nil)
}

func (p *operatorProvider) cancel(stepRunID string) {
	p.mu.Lock()
	cancel, ok := p.inFlight[stepRunID]
	if ok {
		delete(p.inFlight, stepRunID)
	}
	p.mu.Unlock()
	if ok {
		cancel()
	}
}

func (p *operatorProvider) sendEvent(a turnAction, eventType, payload string, shouldNotRetry *bool) {
	fields := map[string]any{
		"workerId":          p.workerID,
		"jobId":             a.jobID,
		"jobRunId":          a.jobRunID,
		"taskId":            a.stepID,
		"taskRunExternalId": a.stepRunID,
		"actionId":          a.actionID,
		"eventTimestamp":    time.Now().UTC().Format(time.RFC3339Nano),
		"eventType":         eventType,
		"eventPayload":      payload,
		"retryCount":        a.retryCount,
	}
	if shouldNotRetry != nil {
		fields["shouldNotRetry"] = *shouldNotRetry
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		fmt.Fprintf(os.Stderr, "brickie: build %s for %s failed: %v\n", eventType, a.stepRunID, err)
		return
	}
	event := newArgFor(p.session.SendStepActionEvent)
	if err := protojson.Unmarshal(raw, event); err != nil {
		fmt.Fprintf(os.Stderr, "brickie: build %s for %s failed: %v\n", eventType, a.stepRunID, err)
		return
	}
	ctx, cancelSend := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelSend()
	if _, err := p.session.SendStepActionEvent(ctx, event); err != nil {
		fmt.Fprintf(os.Stderr, "brickie: send %s for %s failed: %v\n", eventType, a.stepRunID, err)
	}
}
