package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	v0client "github.com/hatchet-dev/hatchet/pkg/client"
	"github.com/hatchet-dev/hatchet/pkg/client/operatorclient"
	"github.com/hatchet-dev/hatchet/pkg/client/types"
)

const (
	actionTypeStartStepRun  = 0
	actionTypeCancelStepRun = 1
)

type turnAction struct {
	tenantID      string
	workflowRunID string
	jobID         string
	jobName       string
	jobRunID      string
	stepID        string
	stepRunID     string
	actionID      string
	payload       string
	retryCount    int32
}

type operatorProvider struct {
	runner   *turnRunner
	v0       v0client.Client
	workerID string
	mu       sync.Mutex
	wg       sync.WaitGroup
	inFlight map[string]context.CancelFunc
}

func operatorActionID() string {
	return strings.ToLower(TaskName + ":" + TaskName)
}

func RunOperatorProvider(ctx context.Context, opts ProviderOpts, slots int) error {
	runner := newTurnRunner(opts)
	v0, err := v0client.New()
	if err != nil {
		return err
	}
	workflow := &types.Workflow{
		Name: TaskName,
		Jobs: map[string]types.WorkflowJob{
			TaskName: {Steps: []types.WorkflowStep{{
				Name:     TaskName,
				ID:       TaskName,
				ActionID: operatorActionID(),
				Timeout:  runner.timeout.String(),
				Retries:  runner.retries,
			}}},
		},
	}
	if err := v0.Admin().PutWorkflow(workflow); err != nil {
		return fmt.Errorf("register workflow: %w", err)
	}
	session, err := v0.Operator().Connect(ctx, &operatorclient.ConnectRequest{
		Name:       "brickie-provider",
		SlotConfig: map[string]int32{"default": int32(slots)},
	})
	if err != nil {
		return fmt.Errorf("connect operator (SERVER_GRPC_OPERATORS_ENABLED must be set for the tenant): %w", err)
	}
	defer func() {
		_ = session.Close()
	}()
	session.AddActions(operatorActionID())
	if err := session.Flush(ctx); err != nil {
		return fmt.Errorf("flush action set: %w", err)
	}
	assigned, errCh, err := session.Actions(ctx)
	if err != nil {
		return err
	}
	provider := &operatorProvider{
		runner:   runner,
		v0:       v0,
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
				tenantID:      action.TenantId,
				workflowRunID: action.WorkflowRunId,
				jobID:         action.JobId,
				jobName:       action.JobName,
				jobRunID:      action.JobRunId,
				stepID:        action.TaskId,
				stepRunID:     action.TaskRunExternalId,
				actionID:      action.ActionId,
				payload:       action.ActionPayload,
				retryCount:    action.RetryCount,
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

	p.sendEvent(a, v0client.ActionEventTypeStarted, "", nil)
	var payload struct {
		Input TurnInput `json:"input"`
	}
	if err := json.Unmarshal([]byte(a.payload), &payload); err != nil {
		noRetry := true
		p.sendEvent(a, v0client.ActionEventTypeFailed, fmt.Sprintf("decode turn input: %v", err), &noRetry)
		return
	}
	log := func(msg string) {
		retry := a.retryCount
		_ = p.v0.Event().PutLog(context.Background(), a.stepRunID, msg, nil, &retry)
	}
	result, err := p.runner.run(tctx, a.stepRunID, payload.Input, log)
	if err != nil {
		p.sendEvent(a, v0client.ActionEventTypeFailed, err.Error(), nil)
		return
	}
	p.sendEvent(a, v0client.ActionEventTypeCompleted, result, nil)
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

func (p *operatorProvider) sendEvent(a turnAction, eventType v0client.ActionEventType, payload any, shouldNotRetry *bool) {
	ts := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := p.v0.Dispatcher().SendStepActionEvent(ctx, &v0client.ActionEvent{
		Action: &v0client.Action{
			WorkerId:      p.workerID,
			TenantId:      a.tenantID,
			WorkflowRunId: a.workflowRunID,
			JobId:         a.jobID,
			JobName:       a.jobName,
			JobRunId:      a.jobRunID,
			StepId:        a.stepID,
			StepRunId:     a.stepRunID,
			ActionId:      a.actionID,
			RetryCount:    a.retryCount,
		},
		EventTimestamp: &ts,
		EventType:      eventType,
		EventPayload:   payload,
		ShouldNotRetry: shouldNotRetry,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "brickie: send %s for %s failed: %v\n", eventType, a.stepRunID, err)
	}
}
