package gateway

import (
 "context"
 "fmt"
 "os/exec"
 "strings"
)

func runVPPCTLTelemetryCommand(ctx context.Context, binary string, args ...string) (string, error) {
 output, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
 if err != nil {
  return "", fmt.Errorf("vppctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
 }
 return string(output), nil
}
