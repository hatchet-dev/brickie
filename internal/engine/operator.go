package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/hatchet-dev/hatchet/pkg/client/operatorclient"
	"github.com/hatchet-dev/hatchet/pkg/operator"
	"github.com/hatchet-dev/hatchet/pkg/operator/hostgrpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/hatchet-dev/brickie/internal/logging"
)

const (
	teardownTimeout = 30 * time.Second
	actionID        = TaskName + ":" + TaskName
)

type operatorProvider struct {
	runner   *turnRunner
	session  operator.Session
	ready    chan struct{}
	mu       sync.Mutex
	wg       sync.WaitGroup
	inFlight map[string]context.CancelFunc
}

func RunOperatorProvider(ctx context.Context, opts ProviderOpts, slots int) error {
	runner := newTurnRunner(opts)
	token := os.Getenv("HATCHET_CLIENT_TOKEN")
	if token == "" {
		return errors.New("HATCHET_CLIENT_TOKEN is not set")
	}
	exchange, err := hostgrpc.NewStaticExchange(token)
	if err != nil {
		return err
	}
	host, err := hostgrpc.New(hostgrpc.WithTokenSource(exchange), hostgrpc.WithLogger(logging.Hatchet()))
	if err != nil {
		return err
	}
	defer host.Close()

	provider := &operatorProvider{
		runner:   runner,
		ready:    make(chan struct{}),
		inFlight: map[string]context.CancelFunc{},
	}
	session, err := host.Open(ctx, operator.Identity{
		TenantId: exchange.TenantId(),
		Name:     "brickie-provider",
	}, operator.OpenOpts{
		Handler:    provider,
		Actions:    []string{actionID},
		SlotConfig: map[string]int32{"default": int32(min(slots, 10000))}, // #nosec G115 -- bounded above
	})
	if err != nil {
		return fmt.Errorf("open operator session (the tenant needs SERVER_GRPC_OPERATORS_ENABLED and Hatchet v0.110.0 or newer): %w", err)
	}
	provider.session = session
	close(provider.ready)

	workflow := &operatorclient.CreateWorkflowVersionRequest{
		Name: TaskName,
		Tasks: []*operatorclient.CreateTaskOpts{{
			ReadableId: TaskName,
			Action:     actionID,
			Timeout:    turnExecutionTimeout.String(),
			Retries:    turnRetries,
		}},
	}
	actions, err := session.PutWorkflow(ctx, workflow)
	if err != nil {
		return fmt.Errorf("register workflow: %w", err)
	}
	if err := session.AddActions(ctx, actions); err != nil {
		return err
	}
	if err := session.Flush(ctx); err != nil {
		return fmt.Errorf("flush action set: %w", err)
	}
	slog.Info("sandbox provider connected as grpc operator", "worker", session.Registration().WorkerId)

	select {
	case <-ctx.Done():
		teardownCtx, cancel := context.WithTimeout(context.Background(), teardownTimeout)
		defer cancel()
		_ = session.Pause(teardownCtx)
		provider.wg.Wait()
		_ = session.Close(teardownCtx)
		return ctx.Err()
	case <-session.Done():
		provider.wg.Wait()
		return fmt.Errorf("operator session ended: %w", session.Err())
	}
}

func (p *operatorProvider) HandleAction(_ context.Context, action *operatorclient.AssignedAction) error {
	switch action.ActionType {
	case operatorclient.ActionTypeStartStepRun:
		p.wg.Add(1)
		go p.execute(action) // #nosec G118 -- a turn outlives the handler callback; execute scopes its own context
	case operatorclient.ActionTypeCancelStepRun:
		p.cancel(action.TaskRunExternalId)
	}
	return nil
}

func (p *operatorProvider) execute(action *operatorclient.AssignedAction) {
	defer p.wg.Done()
	<-p.ready
	tctx, cancel := context.WithTimeout(context.Background(), turnExecutionTimeout)
	p.mu.Lock()
	p.inFlight[action.TaskRunExternalId] = cancel
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.inFlight, action.TaskRunExternalId)
		p.mu.Unlock()
		cancel()
	}()

	p.sendEvent(action, operatorclient.StepEventTypeStarted, "", nil)
	var payload struct {
		Input TurnInput `json:"input"`
	}
	if err := json.Unmarshal([]byte(action.ActionPayload), &payload); err != nil {
		noRetry := true
		p.sendEvent(action, operatorclient.StepEventTypeFailed, fmt.Sprintf("decode turn input: %v", err), &noRetry)
		return
	}
	log := func(msg string) {
		slog.Info(msg, "run", action.TaskRunExternalId)
	}
	result, err := p.runner.run(tctx, action.TaskRunExternalId, payload.Input, log)
	if err != nil {
		if tctx.Err() != nil {
			p.sendEvent(action, operatorclient.StepEventTypeCancelled, "cancelled", nil)
			return
		}
		p.sendEvent(action, operatorclient.StepEventTypeFailed, err.Error(), nil)
		return
	}
	out, err := json.Marshal(result)
	if err != nil {
		p.sendEvent(action, operatorclient.StepEventTypeFailed, fmt.Sprintf("encode turn result: %v", err), nil)
		return
	}
	p.sendEvent(action, operatorclient.StepEventTypeCompleted, string(out), nil)
}

func (p *operatorProvider) cancel(taskRunExternalID string) {
	p.mu.Lock()
	cancel, ok := p.inFlight[taskRunExternalID]
	if ok {
		delete(p.inFlight, taskRunExternalID)
	}
	p.mu.Unlock()
	if ok {
		cancel()
	}
}

func (p *operatorProvider) sendEvent(action *operatorclient.AssignedAction, eventType operatorclient.StepActionEventType, payload string, shouldNotRetry *bool) {
	retryCount := action.RetryCount
	event := &operatorclient.StepActionEvent{
		JobId:             action.JobId,
		JobRunId:          action.JobRunId,
		TaskId:            action.TaskId,
		TaskRunExternalId: action.TaskRunExternalId,
		ActionId:          action.ActionId,
		EventTimestamp:    timestamppb.Now(),
		EventType:         eventType,
		EventPayload:      payload,
		RetryCount:        &retryCount,
		ShouldNotRetry:    shouldNotRetry,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := p.session.SendStepActionEvent(ctx, event); err != nil {
		slog.Error("send step event failed", "event", eventType, "run", action.TaskRunExternalId, "err", err)
	}
}
