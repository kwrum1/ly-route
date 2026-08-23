package service

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	recordSnapshot, err := controller.captureFileSnapshot(controller.applyRecordPath(service))
	if err != nil {
		return fmt.Errorf("capture %s prior apply record: %w", service, err)
	}
	recordSnapshot = controller.onlyConsistentApplyRecord(service, recordSnapshot)
	// A failed apply restores the rendered files, but it must also restore the
	// receipt that describes those files. Leaving a receipt for the failed
	// generation makes the next status/readback pass report a phantom apply and
	// causes every later transaction to retry the same stale state.
	restoreFailedApply := func(applyErr error, reapplyService bool) error {
		recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		artifactErr := controller.restoreArtifacts(snapshot)
		var serviceErr error
		if artifactErr == nil && reapplyService {
			serviceErr = controller.restoreServiceFromSnapshot(recoveryCtx, service, snapshot, artifacts)
		}
		var recordErr error
		if artifactErr == nil && serviceErr == nil {
			recordErr = controller.restoreFileSnapshot(recordSnapshot)
		} else {
			recordErr = os.Remove(controller.applyRecordPath(service))
			if errors.Is(recordErr, os.ErrNotExist) {
				recordErr = nil
			}
		}
		cleanupErr := controller.removeTransactionMetadata(service)
		return errors.Join(applyErr, artifactErr, serviceErr, recordErr, cleanupErr)
	}
	persistOnly := artifactsArePersistOnly(artifacts)
	// Runtime reconciliation is also used for short-lived DNS classification
	// updates. Do not bounce healthy daemons or replay an unchanged Linux
	// handoff when their rendered generation is already active. PPPoE keeps its
	// dedicated path below because it must also repair target enablement without
	// tearing down a connected native session.
	if service != PPPoE && !persistOnly && artifactsMatchSnapshot(controller, snapshot, artifacts) &&
		(service != Kea || controller.serviceRuntimeIdentityMatches(service, artifacts)) {
		if readbackErr := liveReadback(ctx, controller.Runner, service, artifacts); readbackErr == nil {
			return controller.saveApplyRecord(service, artifacts, transactionIDFromContext(ctx))
		}
	}
	if err := controller.saveRollbackSnapshot(service, snapshot); err != nil {
		return fmt.Errorf("save %s rollback snapshot: %w", service, err)
	}
	if err := controller.writeArtifacts(service, artifacts); err != nil {
		return restoreFailedApply(err, false)
	}
	// An unchanged native PPPoE plan must not tear down a live session. A
	// needless stop/start races the AC and makes policy routing observe a
	// missing pppoe_session. Restart only after a plan change or loss of the
	// native session.
	skipPPPoEApply := service == PPPoE && artifactsMatchSnapshot(controller, snapshot, artifacts) && nativePPPoEArtifactsReady(ctx, controller.Runner, artifacts)
	if persistOnly {
		if err := controller.verifyPersistedArtifacts(artifacts); err != nil {
			return restoreFailedApply(err, false)
		}
	} else {
		if !skipPPPoEApply {
			if err := controller.runApplyCommand(ctx, service, artifacts); err != nil {
				return restoreFailedApply(err, true)
			}
		} else {
			// The peer can remain connected while its target unit is inactive
			// (for example after a manual recovery). Activate the target without
			// restarting the live native session so readback reports a healthy
			// service and boot ordering is repaired.
			for _, unit := range applyUnits(service, artifacts) {
				if err := controller.Runner.Run(ctx, "systemctl", "enable", unit); err != nil {
					return restoreFailedApply(err, false)
				}
			}
			if err := controller.Runner.Run(ctx, "systemctl", "start", applyUnit(service)); err != nil {
				return restoreFailedApply(err, false)
			}
		}
		if err := liveReadback(ctx, controller.Runner, service, artifacts); err != nil {
			if service == PPPoE {
				// A failed PPPoE readback must not leave a retrying or stale session.
				// Stop all rendered peers and verify VPP has no residual session.
				stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
				stopErr := controller.Stop(stopCtx, service, artifacts)
				cancel()
				return restoreFailedApply(errors.Join(fmt.Errorf("%s live readback: %w", service, err), stopErr), false)
			}
			return restoreFailedApply(fmt.Errorf("%s live readback: %w", service, err), true)
		}
	}
	if err := controller.saveApplyRecord(service, artifacts, transactionIDFromContext(ctx)); err != nil {
		return restoreFailedApply(err, !persistOnly)
	}
	return nil
}

func (controller FilesystemController) rollbackWithSnapshot(ctx context.Context, service ServiceName, artifacts []RenderedArtifact) error {
	if controller.Runner == nil {
		return fmt.Errorf("%s rollback requires a daemon command runner", service)
	}
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	cleanup := func() error {
		var cleanupErrors []error
		for _, path := range []string{controller.rollbackSnapshotPath(service), controller.applyRecordPath(service)} {
			if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				cleanupErrors = append(cleanupErrors, removeErr)
			}
		}
		return errors.Join(cleanupErrors...)
	}
	snapshot, err := controller.loadRollbackSnapshot(service)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.Join(fmt.Errorf("load %s rollback snapshot: %w", service, err), cleanup())
	}
	if errors.Is(err, os.ErrNotExist) {
		if err := controller.writeArtifacts(service, artifacts); err != nil {
			return errors.Join(err, cleanup())
		}
	} else if err := controller.restoreArtifacts(snapshot); err != nil {
		return errors.Join(err, cleanup())
	}
	if !artifactsArePersistOnly(artifacts) {
		if err := controller.runApplyCommand(rollbackCtx, service, artifacts); err != nil {
			// A rollback is not a committed generation. Even when the old
			// daemon cannot be restarted, remove both metadata files so the
			// next apply cannot mistake the failed rollback for live state.
			return errors.Join(err, cleanup())
		}
	}
	return cleanup()
}

func (controller FilesystemController) verifyPersistedArtifacts(artifacts []RenderedArtifact) error {
	for _, artifact := range artifacts {
		path, err := controller.resolvePath(artifact.Path)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read persisted artifact %s: %w", artifact.Path, err)
		}
		if string(content) != artifact.Content {
			return fmt.Errorf("persisted artifact %s does not match the committed runtime plan", artifact.Path)
		}
	}
	return nil
}

func (controller FilesystemController) runApplyCommand(ctx context.Context, service ServiceName, artifacts []RenderedArtifact) error {
	if helper := directApplyHelper(service); helper != "" {
		return controller.Runner.Run(ctx, helper)
	}
	if service == Kea {
		unit := applyUnit(service)
		if err := controller.Runner.Run(ctx, "systemctl", "stop", unit); err != nil {
			return err
		}
		// Kea's lease-file cleanup can leave this lock behind when the daemon is
		// interrupted. With the service stopped, the lock is necessarily stale;
		// preserving the lease database while removing it makes restart idempotent.
		pidPath, err := controller.resolvePath("/var/lib/kea/kea-leases4.csv.pid")
		if err != nil {
			return err
		}
		if err := os.Remove(pidPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale Kea lease cleanup lock: %w", err)
		}
		return controller.Runner.Run(ctx, "systemctl", "start", unit)
	}
	if service == PPPoE {
		if err := controller.Runner.Run(ctx, "systemctl", "start", applyUnit(service)); err != nil {
			return err
		}
		// Persist every configured peer under the enabled target. Starting an
		// instance directly is not enough: systemd otherwise forgets the peer on
		// reboot even though its configuration remains on disk.
		for _, unit := range applyUnits(service, artifacts) {
			if err := controller.Runner.Run(ctx, "systemctl", "enable", unit); err != nil {
				return err
			}
			if err := controller.Runner.Run(ctx, "systemctl", "restart", unit); err != nil {
				return err
			}
		}
		return nil
	}
	for _, unit := range applyUnits(service, artifacts) {
		if err := controller.Runner.Run(ctx, "systemctl", applyCommand(service), unit); err != nil {
			return err
		}
	}
	return nil
}

func artifactsMatchSnapshot(controller FilesystemController, snapshots []artifactSnapshot, artifacts []RenderedArtifact) bool {
	if len(snapshots) != len(artifacts) {
		return false
	}
	byPath := make(map[string]artifactSnapshot, len(snapshots))
	for _, snapshot := range snapshots {
		byPath[snapshot.Path] = snapshot
	}
	for _, artifact := range artifacts {
		path, err := controller.resolvePath(artifact.Path)
		if err != nil {
			return false
		}
		snapshot, ok := byPath[path]
		if !ok || !snapshot.Existed || !artifactContentsEqual(snapshot.Content, []byte(artifact.Content)) {
			return false
		}
	}
	return true
}

func artifactContentsEqual(left, right []byte) bool {
	if bytes.Equal(left, right) {
		return true
	}
	// JSON artifacts can be rewritten by an older control-plane version with
	// different indentation or object-key order. Treat semantically identical
	// PPPoE plans as unchanged so an Apply does not needlessly tear down a live
	// native session.
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	leftCanonical, leftErr := json.Marshal(leftValue)
	rightCanonical, rightErr := json.Marshal(rightValue)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftCanonical, rightCanonical)
}

func nativePPPoEArtifactsReady(ctx context.Context, runner CommandRunner, artifacts []RenderedArtifact) bool {
	peerCount := 0
	for _, artifact := range artifacts {
		if !strings.HasPrefix(artifact.Path, "/etc/ly-route/pppoe/ly-route-") {
			continue
		}
		instance := strings.TrimSuffix(filepath.Base(artifact.Path), ".json")
		unit := "ly-route-pppoe@" + instance + ".service"
		state, err := runner.Output(ctx, "systemctl", "is-active", unit)
		if err != nil || strings.TrimSpace(state) != "active" {
			return false
		}
		var plan struct {
			StatusFile string `json:"status_file"`
		}
		if json.Unmarshal([]byte(artifact.Content), &plan) != nil || plan.StatusFile == "" {
			return false
		}
		peerCount++
		if err := validateNativePPPoEOnce(ctx, runner, plan.StatusFile); err != nil {
			return false
		}
	}
	return peerCount > 0
}

func (controller FilesystemController) restoreServiceFromSnapshot(ctx context.Context, service ServiceName, snapshots []artifactSnapshot, current []RenderedArtifact) error {
	if service != PPPoE {
		return controller.runApplyCommand(ctx, service, current)
	}
	previous := make([]RenderedArtifact, 0, len(snapshots))
	for _, snapshot := range snapshots {
		if !snapshot.Existed {
			continue
		}
		path := snapshot.Path
		if controller.RootDir != "" {
			root, rootErr := filepath.Abs(controller.RootDir)
			absolute, absoluteErr := filepath.Abs(snapshot.Path)
			if rootErr != nil || absoluteErr != nil {
				return fmt.Errorf("resolve PPPoE rollback artifact path %s", snapshot.Path)
			}
			relative, relErr := filepath.Rel(root, absolute)
			if relErr != nil || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || relative == ".." {
				return fmt.Errorf("PPPoE rollback artifact escapes controller root: %s", snapshot.Path)
			}
			path = "/" + filepath.ToSlash(relative)
		}
		previous = append(previous, NewArtifact(service, path, string(snapshot.Content), "restart"))
	}
	if len(previous) == 0 {
		return controller.Stop(ctx, service, current)
	}
	return controller.runApplyCommand(ctx, service, previous)
}

func (controller FilesystemController) Stop(ctx context.Context, service ServiceName, artifacts []RenderedArtifact) error {
	if controller.Runner == nil {
		return fmt.Errorf("%s stop requires a daemon command runner", service)
	}
	if service != PPPoE {
		return fmt.Errorf("verified stop is not implemented for %s", service)
	}
	units := applyUnits(service, artifacts)
	if len(units) == 0 {
		return fmt.Errorf("PPPoE stop requires at least one rendered peer")
	}
	var stopErrors []error
	for _, unit := range units {
		if err := controller.Runner.Run(ctx, "systemctl", "disable", "--now", unit); err != nil {
			stopErrors = append(stopErrors, fmt.Errorf("disable %s: %w", unit, err))
		}
	}
	if err := controller.Runner.Run(ctx, "systemctl", "stop", applyUnit(service)); err != nil {
		stopErrors = append(stopErrors, fmt.Errorf("stop %s: %w", applyUnit(service), err))
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
	if service != PPPoE {
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
		for _, managed := range []struct {
			dir, prefix, suffix string
		}{
			{dir: "/etc/smartdns/conf.d", prefix: "ly-route-", suffix: ".conf"},
			{dir: "/etc/smartdns/domain-sets", prefix: "lyroute-", suffix: ".list"},
		} {
			dir, err := controller.resolvePath(managed.dir)
			if err != nil {
				return nil, err
			}
			entries, err := os.ReadDir(dir)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), managed.prefix) && strings.HasSuffix(entry.Name(), managed.suffix) {
					paths[filepath.Join(dir, entry.Name())] = struct{}{}
				}
			}
		}
	}
	snapshots := make([]artifactSnapshot, 0, len(paths))
	for path := range paths {
		snapshot, err := controller.captureFileSnapshot(path)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, nil
}

func (controller FilesystemController) captureFileSnapshot(path string) (artifactSnapshot, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return artifactSnapshot{Path: path}, nil
	}
	if err != nil {
		return artifactSnapshot{}, err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return artifactSnapshot{}, err
	}
	return artifactSnapshot{Path: path, Content: content, Mode: info.Mode().Perm(), Existed: true}, nil
}

func (controller FilesystemController) onlyConsistentApplyRecord(service ServiceName, snapshot artifactSnapshot) artifactSnapshot {
	if !snapshot.Existed {
		return snapshot
	}
	var record serviceApplyRecord
	if err := json.Unmarshal(snapshot.Content, &record); err != nil || record.Service != service || len(record.Artifacts) == 0 {
		return artifactSnapshot{Path: snapshot.Path}
	}
	for artifactPath, expectedHash := range record.Artifacts {
		path, err := controller.resolvePath(artifactPath)
		if err != nil {
			return artifactSnapshot{Path: snapshot.Path}
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return artifactSnapshot{Path: snapshot.Path}
		}
		digest := sha256.Sum256(content)
		if fmt.Sprintf("%x", digest[:]) != expectedHash {
			return artifactSnapshot{Path: snapshot.Path}
		}
	}
	return snapshot
}

func (controller FilesystemController) restoreArtifacts(snapshots []artifactSnapshot) error {
	var restoreErrors []error
	for _, snapshot := range snapshots {
		if err := controller.restoreFileSnapshot(snapshot); err != nil {
			restoreErrors = append(restoreErrors, err)
		}
	}
	return errors.Join(restoreErrors...)
}

func (controller FilesystemController) restoreFileSnapshot(snapshot artifactSnapshot) error {
	if !snapshot.Existed {
		if err := os.Remove(snapshot.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return writeFileAtomically(snapshot.Path, snapshot.Content, snapshot.Mode)
}

func (controller FilesystemController) removeTransactionMetadata(service ServiceName) error {
	var removeErrors []error
	for _, path := range []string{controller.rollbackSnapshotPath(service)} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			removeErrors = append(removeErrors, err)
		}
	}
	return errors.Join(removeErrors...)
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
