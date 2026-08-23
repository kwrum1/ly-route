package vpp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const defaultPPPoERuntimeStatusDir = "/run/ly-route/pppoe"

type pppoeRuntimeStatus struct {
	Interface string `json:"interface"`
}

func resolveRuntimePPPoECommand(command string) (string, error) {
	fields := strings.Fields(command)
	for index, field := range fields {
		resolved, err := resolveRuntimePPPoEInterface(field)
		if err != nil {
			return "", err
		}
		fields[index] = resolved
	}
	return strings.Join(fields, " "), nil
}

func resolveRuntimePPPoEInterface(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "pppoe-runtime:") {
		return value, nil
	}
	peerID := strings.TrimPrefix(value, "pppoe-runtime:")
	if peerID == "" || strings.ContainsAny(peerID, "/\\ \t\r\n") {
		return "", fmt.Errorf("invalid PPPoE runtime peer %q", peerID)
	}
	statusDir := strings.TrimSpace(os.Getenv("LY_ROUTE_PPPOE_RUNTIME_STATUS_DIR"))
	if statusDir == "" {
		statusDir = defaultPPPoERuntimeStatusDir
	}
	data, err := os.ReadFile(statusDir + "/" + peerID + ".json")
	if err != nil {
		return "", fmt.Errorf("read PPPoE runtime peer %q: %w", peerID, err)
	}
	var status pppoeRuntimeStatus
	if err := json.Unmarshal(data, &status); err != nil {
		return "", fmt.Errorf("decode PPPoE runtime peer %q: %w", peerID, err)
	}
	if !strings.HasPrefix(status.Interface, "pppoe_session") {
		return "", fmt.Errorf("PPPoE runtime peer %q has invalid interface %q", peerID, status.Interface)
	}
	return status.Interface, nil
}

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
			batchResults, batchErr := channel.doVPPRouteBatch(ctx, batch)
			results = append(results, batchResults...)
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
		resolvedCommand, resolveErr := resolveRuntimePPPoECommand(command)
		if resolveErr != nil {
			if ignoreFailure {
				results = append(results, VPPCTLCommandResult{Command: logicalCommand, Stderr: resolveErr.Error(), Retval: 1})
				continue
			}
			return Reply{Operation: operation.Name, Retval: 1, Payload: VPPCTLReplyPayload{CommandResults: results}}, resolveErr
		}
		command = resolvedCommand
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

// Keep large provider datasets in bounded VPP CLI files without paying one
// process launch for every few prefixes. A 512-command batch stays well below
// practical exec-file limits while making full GeoIP replay fast enough for a
// control-plane transaction.
const vppRouteBatchChunkSize = 512

func (channel vppctlChannel) doVPPRouteBatch(ctx context.Context, commands []string) ([]VPPCTLCommandResult, error) {
	results := make([]VPPCTLCommandResult, 0, (len(commands)+vppRouteBatchChunkSize-1)/vppRouteBatchChunkSize)
	for begin := 0; begin < len(commands); begin += vppRouteBatchChunkSize {
		end := begin + vppRouteBatchChunkSize
		if end > len(commands) {
			end = len(commands)
		}
		result, err := channel.doVPPRouteBatchChunk(ctx, commands[begin:end], begin, end, len(commands))
		results = append(results, result)
		if err != nil {
			return results, err
		}
	}
	return results, nil
}

func (channel vppctlChannel) doVPPRouteBatchChunk(ctx context.Context, commands []string, begin, end, total int) (VPPCTLCommandResult, error) {
	label := fmt.Sprintf("vpp route command batch %d-%d/%d", begin+1, end, total)
	file, err := os.CreateTemp("", "ly-route-vpp-batch-*.conf")
	if err != nil {
		return VPPCTLCommandResult{Command: label}, fmt.Errorf("create %s: %w", label, err)
	}
	path := file.Name()
	defer os.Remove(path)
	for _, command := range commands {
		if lanInterface := strings.TrimSpace(os.Getenv("LY_ROUTE_LAN_INTERFACE")); lanInterface != "" {
			command = strings.ReplaceAll(command, "$LY_ROUTE_LAN_INTERFACE", lanInterface)
		}
		resolvedCommand, err := resolveRuntimePPPoECommand(command)
		if err != nil {
			return VPPCTLCommandResult{Command: label}, err
		}
		command = resolvedCommand
		if _, err := file.WriteString(strings.TrimSpace(command) + "\n"); err != nil {
			_ = file.Close()
			return VPPCTLCommandResult{Command: label}, fmt.Errorf("write %s: %w", label, err)
		}
	}
	if err := file.Close(); err != nil {
		return VPPCTLCommandResult{Command: label}, fmt.Errorf("close %s: %w", label, err)
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, channel.binary, "exec", path)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	result := VPPCTLCommandResult{Command: label, Stdout: stdout.String(), Stderr: stderr.String()}
	if runErr != nil {
		return result, fmt.Errorf("vppctl exec %s failed: %w: %s", label, runErr, strings.TrimSpace(stderr.String()))
	}
	combined := strings.ToLower(stdout.String() + "\n" + stderr.String())
	if strings.Contains(combined, "cli line error") || strings.Contains(combined, "unknown input") || strings.Contains(combined, "parse error") {
		return result, fmt.Errorf("vppctl exec %s reported an error: %s", label, strings.TrimSpace(stdout.String()+" "+stderr.String()))
	}
	return result, nil
}
