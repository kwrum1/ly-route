package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type artifactSnapshot struct {
	Path    string      `json:"path"`
	Content []byte      `json:"content,omitempty"`
	Mode    os.FileMode `json:"mode,omitempty"`
	Existed bool        `json:"existed"`
}

func (controller FilesystemController) applyWithRecovery(ctx context.Context, service ServiceName, artifacts []RenderedArtifact) error {
	if controller.Runner == nil {
		return fmt.Errorf("%s apply requires a daemon command runner", service)
	}
	snapshot, err := controller.captureArtifacts(service, artifacts)
	if err != nil {
		return fmt.Errorf("capture %s prior artifacts: %w", service, err)
	}
	if err := controller.saveRollbackSnapshot(service, snapshot); err != nil {
		return fmt.Errorf("save %s rollback snapshot: %w", service, err)
	}
	if err := controller.writeArtifacts(service, artifacts); err != nil {
		return errors.Join(err, controller.restoreArtifacts(snapshot))
	}
	if err := controller.runApplyCommand(ctx, service, artifacts); err != nil {
		restoreErr := controller.restoreArtifacts(snapshot)
		if restoreErr == nil {
			restoreErr = controller.runApplyCommand(ctx, service, artifacts)
		}
		return errors.Join(err, restoreErr)
	}
	if err := liveReadback(ctx, controller.Runner, service, artifacts); err != nil {
		restoreErr := controller.restoreArtifacts(snapshot)
		if service == PPPd {
			// A failed PPPoE readback must not leave a retrying or stale session.
			// Stop all rendered peers and verify VPP has no residual session.
			stopErr := controller.Stop(ctx, service, artifacts)
			return errors.Join(fmt.Errorf("%s live readback: %w", service, err), restoreErr, stopErr)
		}
		if restoreErr == nil {
			restoreErr = controller.runApplyCommand(ctx, service, artifacts)
		}
		return errors.Join(fmt.Errorf("%s live readback: %w", service, err), restoreErr)
	}
	if err := controller.saveApplyRecord(service, artifacts); err != nil {
		restoreErr := controller.restoreArtifacts(snapshot)
		if restoreErr == nil {
			restoreErr = controller.runApplyCommand(ctx, service, artifacts)
		}
		return errors.Join(err, restoreErr)
	}
	return nil
}

func (controller FilesystemController) rollbackWithSnapshot(ctx context.Context, service ServiceName, artifacts []RenderedArtifact) error {
	if controller.Runner == nil {
		return fmt.Errorf("%s rollback requires a daemon command runner", service)
	}
	snapshot, err := controller.loadRollbackSnapshot(service)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("load %s rollback snapshot: %w", service, err)
	}
	if errors.Is(err, os.ErrNotExist) {
		if err := controller.writeArtifacts(service, artifacts); err != nil {
			return err
		}
	} else if err := controller.restoreArtifacts(snapshot); err != nil {
		return err
	}
	if err := controller.runApplyCommand(ctx, service, artifacts); err != nil {
		return err
	}
	for _, path := range []string{controller.rollbackSnapshotPath(service), controller.applyRecordPath(service)} {
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return removeErr
		}
	}
	return nil
}

func (controller FilesystemController) runApplyCommand(ctx context.Context, service ServiceName, artifacts []RenderedArtifact) error {
	if helper := directApplyHelper(service); helper != "" {
		return controller.Runner.Run(ctx, helper)
	}
	for _, unit := range applyUnits(service, artifacts) {
		if err := controller.Runner.Run(ctx, "systemctl", applyCommand(service), unit); err != nil {
			return err
		}
	}
	return nil
}

func (controller FilesystemController) Stop(ctx context.Context, service ServiceName, artifacts []RenderedArtifact) error {
	if controller.Runner == nil {
		return fmt.Errorf("%s stop requires a daemon command runner", service)
	}
	if service != PPPd {
		return fmt.Errorf("verified stop is not implemented for %s", service)
	}
	units := applyUnits(service, artifacts)
	if len(units) == 0 {
		return fmt.Errorf("PPPoE stop requires at least one rendered peer")
	}
	var stopErrors []error
	for _, unit := range units {
		if err := controller.Runner.Run(ctx, "systemctl", "stop", unit); err != nil {
			stopErrors = append(stopErrors, fmt.Errorf("stop %s: %w", unit, err))
		}
	}
	if err := errors.Join(stopErrors...); err != nil {
		return err
	}
	return controller.waitForNativePPPoESessionsAbsent(ctx)
}

func (controller FilesystemController) waitForNativePPPoESessionsAbsent(ctx context.Context) error {
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		output, err := controller.Runner.Output(ctx, "vppctl", "show", "pppoe", "session")
		if err == nil && strings.Contains(output, "No pppoe sessions configured") {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("native VPP PPPoE session remained after service stop")
		case <-ticker.C:
		}
	}
}

func applyUnits(service ServiceName, artifacts []RenderedArtifact) []string {
	if service != PPPd {
		return []string{applyUnit(service)}
	}
	units := make([]string, 0)
	for _, artifact := range artifacts {
		if strings.HasPrefix(artifact.Path, "/etc/ly-route/pppoe/ly-route-") && strings.HasSuffix(artifact.Path, ".json") {
			instance := strings.TrimSuffix(filepath.Base(artifact.Path), ".json")
			units = append(units, "ly-route-pppoe@"+instance+".service")
		}
	}
	slices.Sort(units)
	return units
}

func (controller FilesystemController) captureArtifacts(service ServiceName, artifacts []RenderedArtifact) ([]artifactSnapshot, error) {
	paths := make(map[string]struct{}, len(artifacts))
	for _, artifact := range artifacts {
		path, err := controller.resolvePath(artifact.Path)
		if err != nil {
			return nil, err
		}
		paths[path] = struct{}{}
	}
	if service == SmartDNS {
		dir, err := controller.resolvePath("/etc/smartdns/conf.d")
		if err != nil {
			return nil, err
		}
		entries, err := os.ReadDir(dir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "ly-route-") && strings.HasSuffix(entry.Name(), ".conf") {
				paths[filepath.Join(dir, entry.Name())] = struct{}{}
			}
		}
	}
	snapshots := make([]artifactSnapshot, 0, len(paths))
	for path := range paths {
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			snapshots = append(snapshots, artifactSnapshot{Path: path})
			continue
		}
		if err != nil {
			return nil, err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, artifactSnapshot{Path: path, Content: content, Mode: info.Mode().Perm(), Existed: true})
	}
	return snapshots, nil
}

func (controller FilesystemController) restoreArtifacts(snapshots []artifactSnapshot) error {
	var restoreErrors []error
	for _, snapshot := range snapshots {
		if !snapshot.Existed {
			if err := os.Remove(snapshot.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
				restoreErrors = append(restoreErrors, err)
			}
			continue
		}
		if err := writeFileAtomically(snapshot.Path, snapshot.Content, snapshot.Mode); err != nil {
			restoreErrors = append(restoreErrors, err)
		}
	}
	return errors.Join(restoreErrors...)
}

func (controller FilesystemController) saveRollbackSnapshot(service ServiceName, snapshots []artifactSnapshot) error {
	payload, err := json.Marshal(snapshots)
	if err != nil {
		return err
	}
	return writeFileAtomically(controller.rollbackSnapshotPath(service), payload, 0o600)
}

func (controller FilesystemController) loadRollbackSnapshot(service ServiceName) ([]artifactSnapshot, error) {
	payload, err := os.ReadFile(controller.rollbackSnapshotPath(service))
	if err != nil {
		return nil, err
	}
	var snapshots []artifactSnapshot
	if err := json.Unmarshal(payload, &snapshots); err != nil {
		return nil, err
	}
	return snapshots, nil
}

func (controller FilesystemController) rollbackSnapshotPath(service ServiceName) string {
	path, _ := controller.resolvePath("/var/lib/ly-route/service-runtime/rollback-" + string(service) + ".json")
	return path
}

func writeFileAtomically(path string, content []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".ly-route-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
