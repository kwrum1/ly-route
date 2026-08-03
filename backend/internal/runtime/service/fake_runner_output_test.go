package service

import (
	"context"
	"strings"
)

func (runner *fakeRunner) Output(_ context.Context, name string, args ...string) (string, error) {
	command := strings.TrimSpace(name + " " + strings.Join(args, " "))
	runner.commands = append(runner.commands, command)
	if err := runner.runErrs[command]; err != nil {
		return "", err
	}
	return runner.outputs[command], nil
}
