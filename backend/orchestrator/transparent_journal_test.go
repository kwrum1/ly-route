package orchestrator

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"ly-route/backend/internal/runtime/vpp"
)

func TestTransparentTransactionJournalRoundTripAndDurableClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "transparent-transaction.json")
	previous := []vpp.BondState{{Name: "lyroute-bond-old", Mode: "active-backup", Members: []string{"lyroute-eth1", "lyroute-eth2"}}}
	desired := []vpp.BondState{{Name: "lyroute-bond-new", Mode: "active-backup", Members: []string{"lyroute-eth3", "lyroute-eth4"}}}
	runtime := &productionOrchestratorRuntime{journalPath: path}
	if err := runtime.beginTransparentTransaction("txn-crash-window", "apply", "generation-1", previous, desired); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("journal permissions = %o, want 600", info.Mode().Perm())
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temporary journal remains after atomic rename: %v", err)
	}

	restarted := &productionOrchestratorRuntime{journalPath: path}
	if err := restarted.loadTransparentTransactionJournal(); err != nil {
		t.Fatal(err)
	}
	if !restarted.transparentJournalPending || !reflect.DeepEqual(restarted.transparentBonds, desired) {
		t.Fatalf("restarted journal state = pending %v bonds %#v", restarted.transparentJournalPending, restarted.transparentBonds)
	}
	if err := restarted.CommitTransparentTransaction(t.Context()); err != nil {
		t.Fatal(err)
	}
	if restarted.transparentJournalPending {
		t.Fatal("journal remains pending after commit")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("journal remains after commit: %v", err)
	}
}

func TestTransparentTransactionJournalRejectsMalformedCrashState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transparent-transaction.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"operation":"apply"}`), 0600); err != nil {
		t.Fatal(err)
	}
	runtime := &productionOrchestratorRuntime{journalPath: path}
	if err := runtime.loadTransparentTransactionJournal(); err == nil {
		t.Fatal("malformed crash journal was accepted")
	}
}
