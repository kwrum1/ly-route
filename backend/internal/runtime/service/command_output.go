package service

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

func (SystemctlRunner) Output(ctx context.Context, name string, args ...string) (string, error) {
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	message := strings.TrimSpace(string(output))
	if err != nil {
		if message == "" {
			message = err.Error()
		}
		return "", fmt.Errorf("%s %s failed: %s", name, strings.Join(args, " "), message)
	}
	return message, nil
}
