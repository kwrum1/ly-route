package vpp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func (channel vppctlChannel) doCommands(ctx context.Context, operation Operation) (Reply, error) {
	results := make([]VPPCTLCommandResult, 0, len(operation.VPPCtlCommands))
	for index := 0; index < len(operation.VPPCtlCommands); index++ {
		command := strings.TrimSpace(operation.VPPCtlCommands[index])
		ignoreFailure := strings.HasPrefix(command, "?")
		command = strings.TrimSpace(strings.TrimPrefix(command, "?"))
		if command == vppRouteBatchBegin {
			batch, end, err := collectVPPRouteBatch(operation.VPPCtlCommands, index)
			if err != nil {
				return Reply{Operation: operation.Name, Payload: VPPCTLReplyPayload{CommandResults: results}}, err
			}
			batchResult, batchErr := channel.doVPPRouteBatch(ctx, batch)
			results = append(results, batchResult)
			if batchErr != nil {
				return Reply{Operation: operation.Name, Payload: VPPCTLReplyPayload{CommandResults: results}}, batchErr
			}
			index = end
			continue
		}
		logicalCommand := command
		if lanInterface := strings.TrimSpace(os.Getenv("LY_ROUTE_LAN_INTERFACE")); lanInterface != "" {
			command = strings.ReplaceAll(command, "$LY_ROUTE_LAN_INTERFACE", lanInterface)
		}
		if command == "" {
			continue
		}
		args := strings.Fields(command)
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		cmd := exec.CommandContext(ctx, channel.binary, args...)
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		retval := int32(0)
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				retval = int32(exitErr.ExitCode())
			} else {
				retval = -1
			}
		}
		results = append(results, VPPCTLCommandResult{Command: logicalCommand, Stdout: stdout.String(), Stderr: stderr.String(), Retval: retval})
		if err != nil {
			if ignoreFailure {
				continue
			}
			reply := Reply{Operation: operation.Name, Retval: retval, Payload: VPPCTLReplyPayload{CommandResults: results}}
			failure := fmt.Errorf("vppctl %s command %q failed with retval %d: %w: %s", operation.Name, command, retval, err, strings.TrimSpace(stderr.String()))
			if strings.HasSuffix(operation.Name, ".snapshot") {
				return reply, fmt.Errorf("%w: %v", ErrSnapshotIncomplete, failure)
			}
			return reply, failure
		}
	}
	payload := VPPCTLReplyPayload{CommandResults: results}
	readback, err := decodeVPPCTLReadback(operation, results)
	if err != nil {
		return Reply{Operation: operation.Name, Payload: payload}, err
	}
	payload.Readback = readback
	return Reply{Operation: operation.Name, Payload: payload}, nil
}

func collectVPPRouteBatch(commands []string, begin int) ([]string, int, error) {
	batch := make([]string, 0, 128)
	for index := begin + 1; index < len(commands); index++ {
		command := strings.TrimSpace(commands[index])
		command = strings.TrimSpace(strings.TrimPrefix(command, "?"))
		if command == vppRouteBatchEnd {
			return batch, index, nil
		}
		if command == vppRouteBatchBegin {
			return nil, -1, fmt.Errorf("nested VPP route command batch")
		}
		if command != "" {
			batch = append(batch, command)
		}
	}
	return nil, -1, fmt.Errorf("unterminated VPP route command batch")
}

func (channel vppctlChannel) doVPPRouteBatch(ctx context.Context, commands []string) (VPPCTLCommandResult, error) {
	file, err := os.CreateTemp("", "ly-route-vpp-batch-*.conf")
	if err != nil {
		return VPPCTLCommandResult{Command: "vpp route command batch"}, fmt.Errorf("create VPP route command batch: %w", err)
	}
	path := file.Name()
	defer os.Remove(path)
	for _, command := range commands {
		if lanInterface := strings.TrimSpace(os.Getenv("LY_ROUTE_LAN_INTERFACE")); lanInterface != "" {
			command = strings.ReplaceAll(command, "$LY_ROUTE_LAN_INTERFACE", lanInterface)
		}
		if _, err := file.WriteString(strings.TrimSpace(command) + "\n"); err != nil {
			_ = file.Close()
			return VPPCTLCommandResult{Command: "vpp route command batch"}, fmt.Errorf("write VPP route command batch: %w", err)
		}
	}
	if err := file.Close(); err != nil {
		return VPPCTLCommandResult{Command: "vpp route command batch"}, fmt.Errorf("close VPP route command batch: %w", err)
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, channel.binary, "exec", path)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	result := VPPCTLCommandResult{Command: "vpp route command batch", Stdout: stdout.String(), Stderr: stderr.String()}
	if runErr != nil {
		return result, fmt.Errorf("vppctl exec route command batch failed: %w: %s", runErr, strings.TrimSpace(stderr.String()))
	}
	combined := strings.ToLower(stdout.String() + "\n" + stderr.String())
	if strings.Contains(combined, "cli line error") || strings.Contains(combined, "unknown input") || strings.Contains(combined, "parse error") {
		return result, fmt.Errorf("vppctl exec route command batch reported an error: %s", strings.TrimSpace(stdout.String()+" "+stderr.String()))
	}
	return result, nil
}
