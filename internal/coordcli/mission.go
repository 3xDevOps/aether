package coordcli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/3xDevOps/Aether/internal/coordtransport"
	"github.com/3xDevOps/Aether/internal/protocol"
)

const maxMissionSpecBytes = 32 << 10

func missionCommand(ctx context.Context, socket, kind string, args []string, in io.Reader, _ io.Writer) (any, error) {
	if len(args) == 0 {
		return nil, usageError(kind + " requires a subcommand")
	}
	switch kind {
	case "task":
		switch args[0] {
		case "show":
			return taskShow(ctx, socket, args[1:])
		case "list":
			return taskList(ctx, socket, args[1:])
		case "propose":
			return taskPropose(ctx, socket, args[1:], in)
		case "revise":
			return taskRevise(ctx, socket, args[1:], in)
		case "accept":
			return taskAccept(ctx, socket, args[1:])
		case "accept-submission":
			return taskAcceptSubmission(ctx, socket, args[1:])
		case "abandon":
			return taskAbandon(ctx, socket, args[1:])
		default:
			return nil, usageError("unknown task command: " + args[0])
		}
	case "worker":
		switch args[0] {
		case "start":
			return workerStart(ctx, socket, args[1:])
		case "list":
			return workerList(ctx, socket, args[1:])
		case "inspect":
			return workerInspect(ctx, socket, args[1:])
		case "cancel":
			return workerCancel(ctx, socket, args[1:])
		case "retry":
			return workerRetry(ctx, socket, args[1:])
		default:
			return nil, usageError("unknown worker command: " + args[0])
		}
	default:
		return nil, usageError("unknown mission command: " + kind)
	}
}

func taskShow(ctx context.Context, socket string, args []string) (protocol.TaskShowResult, error) {
	fs := newFlags("task show")
	taskID := fs.String("task-id", "", "task ID")
	if err := parseFlags(fs, args); err != nil {
		return protocol.TaskShowResult{}, err
	}
	if *taskID == "" && fs.NArg() == 1 {
		*taskID = fs.Arg(0)
	}
	if *taskID == "" || fs.NArg() > 1 {
		return protocol.TaskShowResult{}, usageError("task show requires --task-id <id>")
	}
	var out protocol.TaskShowResult
	if err := coordtransport.Call(ctx, socket, protocol.MethodTaskShow, protocol.TaskShowParams{TaskID: *taskID}, &out); err != nil {
		return out, err
	}
	return out, nil
}

func taskList(ctx context.Context, socket string, args []string) (protocol.TaskListResult, error) {
	fs := newFlags("task list")
	missionID := fs.String("mission-id", "", "mission ID")
	if err := parseFlags(fs, args); err != nil {
		return protocol.TaskListResult{}, err
	}
	if *missionID == "" || fs.NArg() != 0 {
		return protocol.TaskListResult{}, usageError("task list requires --mission-id <id>")
	}
	var out protocol.TaskListResult
	if err := coordtransport.Call(ctx, socket, protocol.MethodTaskList, protocol.TaskListParams{MissionID: *missionID}, &out); err != nil {
		return out, err
	}
	if out.Tasks == nil {
		out.Tasks = []protocol.Task{}
	}
	return out, nil
}

func taskPropose(ctx context.Context, socket string, args []string, in io.Reader) (protocol.TaskMutationResult, error) {
	fs := newFlags("task propose")
	missionID := fs.String("mission-id", "", "mission ID")
	revisionJSON := fs.String("revision", "", "task revision JSON")
	revisionFile := fs.String("revision-file", "", "read task revision JSON from a file, or - for stdin")
	idempotencyKey := fs.String("idempotency-key", "", "stable key used to replay this mutation")
	if err := parseFlags(fs, args); err != nil {
		return protocol.TaskMutationResult{}, err
	}
	if *missionID == "" || *idempotencyKey == "" || fs.NArg() != 0 {
		return protocol.TaskMutationResult{}, usageError("task propose requires --mission-id, --idempotency-key, and a revision")
	}
	var revision protocol.TaskRevision
	if err := resolveJSON(*revisionJSON, *revisionFile, in, &revision); err != nil {
		return protocol.TaskMutationResult{}, err
	}
	var out protocol.TaskMutationResult
	if err := coordtransport.Call(ctx, socket, protocol.MethodTaskPropose, protocol.TaskProposeParams{MissionID: *missionID, Revision: revision, IdempotencyKey: *idempotencyKey}, &out); err != nil {
		return out, err
	}
	return out, nil
}

func taskRevise(ctx context.Context, socket string, args []string, in io.Reader) (protocol.TaskMutationResult, error) {
	fs := newFlags("task revise")
	taskID := fs.String("task-id", "", "task ID")
	revisionJSON := fs.String("revision", "", "task revision JSON")
	revisionFile := fs.String("revision-file", "", "read task revision JSON from a file, or - for stdin")
	idempotencyKey := fs.String("idempotency-key", "", "stable key used to replay this mutation")
	if err := parseFlags(fs, args); err != nil {
		return protocol.TaskMutationResult{}, err
	}
	if *taskID == "" || *idempotencyKey == "" || fs.NArg() != 0 {
		return protocol.TaskMutationResult{}, usageError("task revise requires --task-id, --idempotency-key, and a revision")
	}
	var revision protocol.TaskRevision
	if err := resolveJSON(*revisionJSON, *revisionFile, in, &revision); err != nil {
		return protocol.TaskMutationResult{}, err
	}
	var out protocol.TaskMutationResult
	if err := coordtransport.Call(ctx, socket, protocol.MethodTaskRevise, protocol.TaskReviseParams{TaskID: *taskID, Revision: revision, IdempotencyKey: *idempotencyKey}, &out); err != nil {
		return out, err
	}
	return out, nil
}

func taskAccept(ctx context.Context, socket string, args []string) (protocol.TaskMutationResult, error) {
	fs := newFlags("task accept")
	taskID := fs.String("task-id", "", "task ID")
	revision := fs.Int("revision", -1, "accepted task revision")
	generation := fs.String("expected-integrator-generation", "", "integrator generation observed by the caller")
	idempotencyKey := fs.String("idempotency-key", "", "stable key used to replay this mutation")
	if err := parseFlags(fs, args); err != nil {
		return protocol.TaskMutationResult{}, err
	}
	gen, err := requiredGeneration(*generation)
	if err != nil {
		return protocol.TaskMutationResult{}, err
	}
	if *taskID == "" || *revision < 0 || *idempotencyKey == "" || fs.NArg() != 0 {
		return protocol.TaskMutationResult{}, usageError("task accept requires --task-id, --revision, --expected-integrator-generation, and --idempotency-key")
	}
	var out protocol.TaskMutationResult
	if err := coordtransport.Call(ctx, socket, protocol.MethodTaskAccept, protocol.TaskAcceptParams{TaskID: *taskID, Revision: *revision, ExpectedIntegratorGeneration: gen, IdempotencyKey: *idempotencyKey}, &out); err != nil {
		return out, err
	}
	return out, nil
}

func taskAcceptSubmission(ctx context.Context, socket string, args []string) (protocol.TaskMutationResult, error) {
	fs := newFlags("task accept-submission")
	submissionID := fs.String("submission-id", "", "submission ID")
	generation := fs.String("expected-integrator-generation", "", "integrator generation observed by the caller")
	acceptedSet := fs.String("expected-accepted-set-version", "", "accepted task set version observed by the caller")
	idempotencyKey := fs.String("idempotency-key", "", "stable key used to replay this mutation")
	scopeDisposition := fs.String("scope-disposition", "", "explicit reason for accepting reported scope violations")
	if err := parseFlags(fs, args); err != nil {
		return protocol.TaskMutationResult{}, err
	}
	gen, err := requiredGeneration(*generation)
	if err != nil {
		return protocol.TaskMutationResult{}, err
	}
	setVersion, err := requiredGeneration(*acceptedSet)
	if err != nil {
		return protocol.TaskMutationResult{}, usageError("expected-accepted-set-version must be an unsigned integer")
	}
	if *submissionID == "" || *idempotencyKey == "" || fs.NArg() != 0 {
		return protocol.TaskMutationResult{}, usageError("task accept-submission requires --submission-id, both expected versions, and --idempotency-key")
	}
	var out protocol.TaskMutationResult
	p := protocol.TaskAcceptSubmissionParams{SubmissionID: *submissionID, ExpectedIntegratorGeneration: gen, ExpectedAcceptedSetVersion: setVersion, IdempotencyKey: *idempotencyKey, ScopeDisposition: *scopeDisposition}
	if err := coordtransport.Call(ctx, socket, protocol.MethodTaskAcceptSubmission, p, &out); err != nil {
		return out, err
	}
	return out, nil
}

func taskAbandon(ctx context.Context, socket string, args []string) (protocol.TaskMutationResult, error) {
	fs := newFlags("task abandon")
	taskID := fs.String("task-id", "", "task ID")
	generation := fs.String("expected-integrator-generation", "", "integrator generation observed by the caller")
	idempotencyKey := fs.String("idempotency-key", "", "stable key used to replay this mutation")
	if err := parseFlags(fs, args); err != nil {
		return protocol.TaskMutationResult{}, err
	}
	gen, err := requiredGeneration(*generation)
	if err != nil {
		return protocol.TaskMutationResult{}, err
	}
	if *taskID == "" || *idempotencyKey == "" || fs.NArg() != 0 {
		return protocol.TaskMutationResult{}, usageError("task abandon requires --task-id, --expected-integrator-generation, and --idempotency-key")
	}
	var out protocol.TaskMutationResult
	if err := coordtransport.Call(ctx, socket, protocol.MethodTaskAbandon, protocol.TaskAbandonParams{TaskID: *taskID, ExpectedIntegratorGeneration: gen, IdempotencyKey: *idempotencyKey}, &out); err != nil {
		return out, err
	}
	return out, nil
}

func workerStart(ctx context.Context, socket string, args []string) (protocol.WorkerStartResult, error) {
	fs := newFlags("worker start")
	missionID := fs.String("mission-id", "", "mission ID")
	taskID := fs.String("task-id", "", "task ID")
	taskRevision := fs.Int("task-revision", -1, "accepted task revision")
	dispatchKey := fs.String("dispatch-key", "", "stable identity for this worker start")
	harness := fs.String("harness", "", "worker harness")
	mode := fs.String("mode", "", "worker launch mode")
	accountOwner := fs.String("account-owner-id", "", "account owner ID")
	runOwner := fs.String("run-owner-id", "", "run owner ID")
	generation := fs.String("expected-integrator-generation", "", "integrator generation observed by the caller")
	if err := parseFlags(fs, args); err != nil {
		return protocol.WorkerStartResult{}, err
	}
	gen, err := requiredGeneration(*generation)
	if err != nil {
		return protocol.WorkerStartResult{}, err
	}
	if *missionID == "" || *taskID == "" || *taskRevision < 0 || *dispatchKey == "" || *harness == "" || *mode == "" || *accountOwner == "" || *runOwner == "" || fs.NArg() != 0 {
		return protocol.WorkerStartResult{}, usageError("worker start requires mission/task/revision, dispatch identity, harness, mode, owners, and expected generation")
	}
	p := protocol.WorkerStartParams{MissionID: *missionID, TaskID: *taskID, TaskRevision: *taskRevision, DispatchKey: *dispatchKey, Harness: *harness, Mode: *mode, AccountOwnerID: *accountOwner, RunOwnerID: *runOwner, ExpectedIntegratorGeneration: gen}
	var out protocol.WorkerStartResult
	if err := coordtransport.Call(ctx, socket, protocol.MethodWorkerStart, p, &out); err != nil {
		return out, err
	}
	return out, nil
}

func workerList(ctx context.Context, socket string, args []string) (protocol.WorkerListResult, error) {
	fs := newFlags("worker list")
	missionID := fs.String("mission-id", "", "mission ID")
	taskID := fs.String("task-id", "", "optional task ID")
	limit := fs.Int("limit", 0, "maximum attempts to return")
	if err := parseFlags(fs, args); err != nil {
		return protocol.WorkerListResult{}, err
	}
	if *missionID == "" || *limit < 0 || fs.NArg() != 0 {
		return protocol.WorkerListResult{}, usageError("worker list requires --mission-id")
	}
	var out protocol.WorkerListResult
	if err := coordtransport.Call(ctx, socket, protocol.MethodWorkerList, protocol.WorkerListParams{MissionID: *missionID, TaskID: *taskID, Limit: *limit}, &out); err != nil {
		return out, err
	}
	if out.Attempts == nil {
		out.Attempts = []protocol.Attempt{}
	}
	return out, nil
}

func workerInspect(ctx context.Context, socket string, args []string) (protocol.WorkerInspectResult, error) {
	fs := newFlags("worker inspect")
	attemptID := fs.String("attempt-id", "", "attempt ID")
	if err := parseFlags(fs, args); err != nil {
		return protocol.WorkerInspectResult{}, err
	}
	if *attemptID == "" || fs.NArg() != 0 {
		return protocol.WorkerInspectResult{}, usageError("worker inspect requires --attempt-id")
	}
	var out protocol.WorkerInspectResult
	if err := coordtransport.Call(ctx, socket, protocol.MethodWorkerInspect, protocol.WorkerInspectParams{AttemptID: *attemptID}, &out); err != nil {
		return out, err
	}
	return out, nil
}

func workerCancel(ctx context.Context, socket string, args []string) (protocol.WorkerMutationResult, error) {
	fs := newFlags("worker cancel")
	attemptID := fs.String("attempt-id", "", "attempt ID")
	generation := fs.String("expected-integrator-generation", "", "integrator generation observed by the caller")
	idempotencyKey := fs.String("idempotency-key", "", "stable key used to replay this mutation")
	if err := parseFlags(fs, args); err != nil {
		return protocol.WorkerMutationResult{}, err
	}
	gen, err := requiredGeneration(*generation)
	if err != nil {
		return protocol.WorkerMutationResult{}, err
	}
	if *attemptID == "" || *idempotencyKey == "" || fs.NArg() != 0 {
		return protocol.WorkerMutationResult{}, usageError("worker cancel requires --attempt-id, --expected-integrator-generation, and --idempotency-key")
	}
	var out protocol.WorkerMutationResult
	if err := coordtransport.Call(ctx, socket, protocol.MethodWorkerCancel, protocol.WorkerCancelParams{AttemptID: *attemptID, ExpectedIntegratorGeneration: gen, IdempotencyKey: *idempotencyKey}, &out); err != nil {
		return out, err
	}
	return out, nil
}

func workerRetry(ctx context.Context, socket string, args []string) (protocol.WorkerMutationResult, error) {
	fs := newFlags("worker retry")
	attemptID := fs.String("attempt-id", "", "attempt ID")
	dispatchKey := fs.String("dispatch-key", "", "stable identity for this retry")
	generation := fs.String("expected-integrator-generation", "", "integrator generation observed by the caller")
	if err := parseFlags(fs, args); err != nil {
		return protocol.WorkerMutationResult{}, err
	}
	gen, err := requiredGeneration(*generation)
	if err != nil {
		return protocol.WorkerMutationResult{}, err
	}
	if *attemptID == "" || *dispatchKey == "" || fs.NArg() != 0 {
		return protocol.WorkerMutationResult{}, usageError("worker retry requires --attempt-id, --dispatch-key, and --expected-integrator-generation")
	}
	var out protocol.WorkerMutationResult
	if err := coordtransport.Call(ctx, socket, protocol.MethodWorkerRetry, protocol.WorkerRetryParams{AttemptID: *attemptID, DispatchKey: *dispatchKey, ExpectedIntegratorGeneration: gen}, &out); err != nil {
		return out, err
	}
	return out, nil
}

func requiredGeneration(value string) (uint64, error) {
	if value == "" {
		return 0, usageError("expected-integrator-generation is required")
	}
	generation, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, usageError("expected-integrator-generation must be an unsigned integer")
	}
	return generation, nil
}

func resolveJSON(raw, file string, in io.Reader, out any) error {
	if raw != "" && file != "" {
		return usageError("choose one of --revision and --revision-file")
	}
	var data []byte
	if file == "" || file == "-" {
		if file == "" {
			if raw == "" {
				return usageError("a revision JSON value or --revision-file is required")
			}
			data = []byte(raw)
		} else {
			var err error
			data, err = io.ReadAll(io.LimitReader(in, maxMissionSpecBytes+1))
			if err != nil {
				return fmt.Errorf("read revision: %w", err)
			}
		}
	} else {
		f, err := os.Open(file)
		if err != nil {
			return fmt.Errorf("read revision file: %w", err)
		}
		defer func() { _ = f.Close() }()
		data, err = io.ReadAll(io.LimitReader(f, maxMissionSpecBytes+1))
		if err != nil {
			return fmt.Errorf("read revision file: %w", err)
		}
	}
	if len(data) > maxMissionSpecBytes {
		return usageError(fmt.Sprintf("revision exceeds %d bytes", maxMissionSpecBytes))
	}
	if err := json.Unmarshal(data, out); err != nil {
		return usageError("invalid revision JSON: " + err.Error())
	}
	return nil
}
