package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestNotifyDependentRuntimeQueuesPolicyReconciliation(t *testing.T) {
	previous := runServiceCommand
	t.Cleanup(func() { runServiceCommand = previous })
	var commands [][]string
	runServiceCommand = func(name string, args ...string) ([]byte, error) {
		commands = append(commands, append([]string{name}, args...))
		if len(commands) == 1 {
			return []byte("active\n"), nil
		}
		return nil, nil
	}
	if err := notifyDependentRuntime("ly-route-policy-routing.service"); err != nil {
		t.Fatal(err)
	}
	want := []string{"systemctl", "--no-block", "try-restart", "ly-route-policy-routing.service"}
	if len(commands) != 2 || !reflect.DeepEqual(commands[1], want) {
		t.Fatalf("reconciliation commands = %#v, want state probe and %#v", commands, want)
	}
}

func TestNotifyDependentRuntimeDoesNotRestartActivatingPolicy(t *testing.T) {
	previous := runServiceCommand
	t.Cleanup(func() { runServiceCommand = previous })
	var commands [][]string
	runServiceCommand = func(name string, args ...string) ([]byte, error) {
		commands = append(commands, append([]string{name}, args...))
		return []byte("activating\n"), nil
	}
	if err := notifyDependentRuntime("ly-route-policy-routing.service"); err != nil {
		t.Fatal(err)
	}
	if len(commands) != 1 || !reflect.DeepEqual(commands[0], []string{"systemctl", "show", "--property=ActiveState", "--value", "ly-route-policy-routing.service"}) {
		t.Fatalf("activating reconciliation commands = %#v", commands)
	}
}

func TestNotifyDependentRuntimeRejectsUnsafeUnitAndReportsFailure(t *testing.T) {
	if err := notifyDependentRuntime("../../unsafe.service"); err == nil {
		t.Fatal("unsafe reconciliation unit was accepted")
	}
	previous := runServiceCommand
	t.Cleanup(func() { runServiceCommand = previous })
	call := 0
	runServiceCommand = func(name string, args ...string) ([]byte, error) {
		call++
		if call == 1 {
			return []byte("active\n"), nil
		}
		return []byte("unit failed"), errors.New("exit status 1")
	}
	if err := notifyDependentRuntime("ly-route-policy-routing.service"); err == nil || !strings.Contains(err.Error(), "unit failed") {
		t.Fatalf("reconciliation failure = %v", err)
	}
}
