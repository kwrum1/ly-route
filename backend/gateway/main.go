package gateway

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ly-route/backend/internal/httpapi"
	"ly-route/backend/internal/persistence"
	"ly-route/backend/internal/product"
	"ly-route/backend/internal/runtime/apply"
	"ly-route/backend/internal/runtime/dataplane"
	serviceRuntime "ly-route/backend/internal/runtime/service"
	"ly-route/backend/internal/runtime/vpp"
)

func Run() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	profile, err := loadProductProfile(product.Gateway().ID().String(), os.Getenv("LY_ROUTE_PRODUCT_PROFILE"))
	if err != nil {
		return fmt.Errorf("load product profile: %w", err)
	}
	host := env("LY_ROUTE_API_HOST", "127.0.0.1")
	port := env("LY_ROUTE_API_PORT", "8080")
	addr := fmt.Sprintf("%s:%s", host, port)
	paths := defaultStatePaths(profile, pathExists)
	databasePath := env("LY_ROUTE_DB_PATH", paths.Database)
	configPath := env("LY_ROUTE_CONFIG_PATH", paths.Config)
	store, err := openStore(context.Background(), databasePath, profile.ID())
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer store.Close()

	selection := product.NewSelection()
	if err := selection.Initialize(profile); err != nil {
		return fmt.Errorf("initialize product selection: %w", err)
	}
	dataplaneController := productionDataplaneController()
	options := []httpapi.Option{
		httpapi.WithVersion(env("LY_ROUTE_VERSION", "dev")),
		httpapi.WithStore(store),
		httpapi.WithAuthConfig(gatewayAuthConfig()),
		httpapi.WithGatewayTransaction(productionGatewayTransactionWithController(dataplaneController)),
		httpapi.WithSmartQoSRuntime(productionSmartQoSObserver{transaction: dataplaneController}, true),
	}
	if envBool("LY_ROUTE_ENABLE_SERVICE_RUNTIME", false) {
		options = append(options, httpapi.WithServiceRuntime(serviceRuntime.Runtime{Controller: serviceRuntime.FilesystemController{RootDir: env("LY_ROUTE_SERVICE_ROOT", ""), Runner: serviceRuntime.SystemctlRunner{}, XrayAPIAddress: env("LY_ROUTE_XRAY_API_ADDRESS", serviceRuntime.DefaultXrayRoutingAPIAddress)}}))
	}
	if profile.AllowsService(product.ServiceKea) {
		options = append(options, httpapi.WithDHCPLeases(serviceRuntime.KeaMemfileLeaseCollector{Path: env("LY_ROUTE_KEA_LEASE_FILE", serviceRuntime.DefaultKeaDHCP4LeaseFile)}))
	}
	if envBool("LY_ROUTE_ENABLE_VPP_INTERFACE_TELEMETRY", true) {
		options = append(options, httpapi.WithInterfaceTelemetry(vppctlInterfaceTelemetry{binary: env("LY_ROUTE_VPPCTL", "vppctl")}))
	}
	options = append(options, httpapi.WithSecurityGuardRuntime(productionSecurityGuardObserver{binary: env("LY_ROUTE_VPPCTL", "vppctl"), now: time.Now}))
	if endpoint := strings.TrimSpace(os.Getenv("LY_ROUTE_GATEWAY_TELEMETRY_URL")); endpoint != "" {
		collector, collectorErr := newGatewayHTTPTelemetry(endpoint)
		if collectorErr != nil {
			return fmt.Errorf("initialize gateway telemetry: %w", collectorErr)
		}
		options = append(options, httpapi.WithGatewayTelemetry(collector))
	}
	server, err := httpapi.NewServer(selection, options...)
	if err != nil {
		return fmt.Errorf("initialize control API: %w", err)
	}
	log.Printf("ly-route %s control API listening on %s database=%s config=%s", profile.ID(), addr, databasePath, configPath)
	if err := httpapi.ListenAndServe(addr, server); err != nil {
		return fmt.Errorf("serve control API: %w", err)
	}
	return nil
}

func gatewayAuthConfig() httpapi.AuthConfig {
	return httpapi.AuthConfig{
		AdminUsername:       env("LY_ROUTE_ADMIN_USERNAME", "admin"),
		AdminPassword:       env("LY_ROUTE_ADMIN_PASSWORD", "password"),
		ReadonlyUsername:    env("LY_ROUTE_READONLY_USERNAME", "readonly"),
		ReadonlyPassword:    env("LY_ROUTE_READONLY_PASSWORD", ""),
		CookieSecure:        envBool("LY_ROUTE_SESSION_COOKIE_SECURE", false),
		ForcePasswordChange: envBool("LY_ROUTE_FORCE_PASSWORD_CHANGE", true),
	}
}

func productionGatewayTransaction() apply.GatewayTransactionRunner {
	return productionGatewayTransactionWithController(productionDataplaneController())
}

func productionGatewayTransactionWithController(controller *dataplane.Transaction) apply.GatewayTransactionRunner {
	vppctl := env("LY_ROUTE_VPPCTL", "vppctl")
	return apply.NewProductionGatewayTransactionWithDataplane(vpp.Adapter{Client: vpp.NewProductionVPPCTLClient(vppctl)}, controller, time.Now)
}

func productionDataplaneController() *dataplane.Transaction {
	host := dataplane.NewLinuxHost()
	host.SysfsRoot = env("LY_ROUTE_SYSFS_ROOT", "/sys")
	host.StartupConfig = env("LY_ROUTE_VPP_STARTUP_CONFIG", "/etc/vpp/startup.conf")
	host.StateDir = env("LY_ROUTE_DATAPLANE_STATE_DIR", "/var/lib/ly-route/dataplane")
	host.VPPCTL = env("LY_ROUTE_VPPCTL", "vppctl")
	return &dataplane.Transaction{Host: host, Now: time.Now}
}

type productionSmartQoSObserver struct {
	transaction *dataplane.Transaction
}

func (observer productionSmartQoSObserver) VerifySmartQoS(ctx context.Context, selected vpp.NativePath) error {
	if observer.transaction == nil || observer.transaction.Host == nil {
		return fmt.Errorf("dataplane runtime is unavailable")
	}
	host, ok := observer.transaction.Host.(*dataplane.LinuxHost)
	if !ok {
		return fmt.Errorf("production Linux dataplane observer is unavailable")
	}
	if selected.Tier == vpp.DataplaneTierDPDK {
		active, found, err := host.LoadActiveState(ctx)
		if err != nil {
			return fmt.Errorf("load active dataplane state: %w", err)
		}
		if !found {
			return fmt.Errorf("active dataplane state is unavailable")
		}
		if !dataplane.SameNativePath(active.Path, selected) {
			return fmt.Errorf("active dataplane path differs from current capability selection")
		}
	}
	return host.VerifySmartQoS(ctx, selected)
}

/* type productionOrchestratorRuntime struct {
	adapter                   vpp.Adapter
	dataplane                 apply.DataplaneController
	transparentMu             sync.Mutex
	transparentBonds          []vpp.BondState
	transparentRequest        string
	transparentGeneration     string
	transparentAppliedAt      time.Time
	journalPath               string
	transparentJournalPending bool
	dpdkMu                    sync.Mutex
	dpdkActive                map[string]vpp.NativePath
	tracker                   *orchestrator.HealthTracker
	probe                     orchestrator.HealthProbe
	capabilityReport          string
	managementInterface       string
}

func (runtime *productionOrchestratorRuntime) ApplyTransparent(ctx context.Context, requestID string, topology orchestrator.Topology, policy *orchestrator.Policy) error {
	runtime.transparentMu.Lock()
	defer runtime.transparentMu.Unlock()
	interfaces := transparentTopologyInterfaces(topology.View())
	path, err := runtime.selectAndPrepare(ctx, requestID, interfaces)
	if err != nil {
		return err
	}
	_ = path
	previousBonds := append([]vpp.BondState(nil), runtime.transparentBonds...)
	desiredBonds := vpp.TransparentOrchestratorBonds(topology.View())
	var policyView *orchestrator.PolicyView
	if policy != nil {
		view := policy.View()
		policyView = &view
	}
	payload, err := json.Marshal(struct {
		Topology orchestrator.TopologyView `json:"topology"`
		Policy   *orchestrator.PolicyView  `json:"policy,omitempty"`
	}{Topology: topology.View(), Policy: policyView})
	if err != nil {
		return fmt.Errorf("encode transparent orchestrator generation: %w", err)
	}
	digest := sha256.Sum256(payload)
	generation := hex.EncodeToString(digest[:])
	if err := runtime.beginTransparentTransaction(requestID, "apply", generation, previousBonds, desiredBonds); err != nil {
		runtime.releaseDPDK(ctx, requestID, err)
		return err
	}
	if len(previousBonds) > 0 || len(desiredBonds) > 0 {
		if _, err := runtime.adapter.ApplyTransparentBondTransition(ctx, requestID+"-bonds", previousBonds, desiredBonds); err != nil {
			runtime.releaseDPDK(ctx, requestID, err)
			return err
		}
	}
	_, err = runtime.adapter.ApplyTransparentOrchestrator(ctx, requestID, vpp.TransparentOrchestratorConfig{Generation: generation, Topology: topology.View(), Policy: policyView})
	if err != nil {
		var rollbackErr error
		if len(previousBonds) > 0 || len(desiredBonds) > 0 {
			_, rollbackErr = runtime.adapter.ApplyTransparentBondTransition(ctx, requestID+"-bond-rollback", desiredBonds, previousBonds)
		}
		runtime.releaseDPDK(ctx, requestID, err)
		if rollbackErr != nil {
			return errors.Join(err, fmt.Errorf("restore transparent orchestrator bonds: %w", rollbackErr))
		}
		if clearErr := runtime.clearTransparentTransactionLocked(); clearErr != nil {
			return errors.Join(err, fmt.Errorf("clear rolled-back transparent transaction: %w", clearErr))
		}
		return err
	}
	runtime.transparentBonds = append([]vpp.BondState(nil), desiredBonds...)
	runtime.transparentRequest = requestID
	runtime.transparentGeneration = generation
	runtime.transparentAppliedAt = time.Now().UTC()
	return nil
}

func (runtime *productionOrchestratorRuntime) DisableTransparent(ctx context.Context, requestID string) error {
	runtime.transparentMu.Lock()
	defer runtime.transparentMu.Unlock()
	if err := runtime.beginTransparentTransaction(requestID, "disable", "", runtime.transparentBonds, nil); err != nil {
		return err
	}
	err := runtime.adapter.DisableTransparentOrchestrator(ctx, requestID)
	if err == nil && len(runtime.transparentBonds) > 0 {
		_, err = runtime.adapter.ApplyTransparentBondTransition(ctx, requestID+"-bonds", runtime.transparentBonds, nil)
		if err == nil {
			runtime.transparentBonds = nil
		}
	}
	if err == nil {
		runtime.transparentRequest = ""
		runtime.transparentGeneration = ""
		runtime.transparentAppliedAt = time.Time{}
	}
	runtime.releaseDPDK(ctx, requestID, err)
	return err
}

type transparentTransactionJournal struct {
	Version       int             `json:"version"`
	TransactionID string          `json:"transaction_id"`
	Operation     string          `json:"operation"`
	Generation    string          `json:"generation,omitempty"`
	PreviousBonds []vpp.BondState `json:"previous_bonds,omitempty"`
	DesiredBonds  []vpp.BondState `json:"desired_bonds,omitempty"`
	StartedAt     time.Time       `json:"started_at"`
}

func (runtime *productionOrchestratorRuntime) beginTransparentTransaction(transactionID, operation, generation string, previous, desired []vpp.BondState) error {
	journal := transparentTransactionJournal{Version: 1, TransactionID: strings.TrimSpace(transactionID), Operation: operation, Generation: generation, PreviousBonds: append([]vpp.BondState(nil), previous...), DesiredBonds: append([]vpp.BondState(nil), desired...), StartedAt: time.Now().UTC()}
	if journal.TransactionID == "" || (journal.Operation != "apply" && journal.Operation != "disable") {
		return errors.New("transparent transaction journal identity is invalid")
	}
	content, err := json.Marshal(journal)
	if err != nil {
		return fmt.Errorf("encode transparent transaction journal: %w", err)
	}
	path := strings.TrimSpace(runtime.journalPath)
	if path == "" {
		return errors.New("transparent transaction journal path is not configured")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return fmt.Errorf("create transparent transaction journal directory: %w", err)
	}
	temporary := path + ".tmp"
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("open transparent transaction journal: %w", err)
	}
	if _, err = file.Write(content); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("write transparent transaction journal: %w", errors.Join(err, closeErr))
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("commit transparent transaction journal: %w", err)
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	runtime.transparentJournalPending = true
	return nil
}

func (runtime *productionOrchestratorRuntime) loadTransparentTransactionJournal() error {
	path := strings.TrimSpace(runtime.journalPath)
	if path == "" {
		return errors.New("transparent transaction journal path is not configured")
	}
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var journal transparentTransactionJournal
	if err := json.Unmarshal(content, &journal); err != nil {
		return fmt.Errorf("decode transparent transaction journal: %w", err)
	}
	if journal.Version != 1 || strings.TrimSpace(journal.TransactionID) == "" || (journal.Operation != "apply" && journal.Operation != "disable") {
		return errors.New("transparent transaction journal is invalid")
	}
	runtime.transparentBonds = append([]vpp.BondState(nil), journal.DesiredBonds...)
	runtime.transparentJournalPending = true
	return nil
}

func (runtime *productionOrchestratorRuntime) CommitTransparentTransaction(context.Context) error {
	runtime.transparentMu.Lock()
	defer runtime.transparentMu.Unlock()
	return runtime.clearTransparentTransactionLocked()
}

func (runtime *productionOrchestratorRuntime) clearTransparentTransactionLocked() error {
	if !runtime.transparentJournalPending {
		return nil
	}
	path := strings.TrimSpace(runtime.journalPath)
	if path == "" {
		return errors.New("transparent transaction journal path is not configured")
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove transparent transaction journal: %w", err)
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	runtime.transparentJournalPending = false
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open transaction journal directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync transaction journal directory: %w", err)
	}
	return nil
}

func (runtime *productionOrchestratorRuntime) TransparentRuntimeEvidence(ctx context.Context) (orchestratorapi.TransparentRuntimeEvidence, error) {
	runtime.transparentMu.Lock()
	defer runtime.transparentMu.Unlock()
	if strings.TrimSpace(runtime.transparentRequest) == "" || strings.TrimSpace(runtime.transparentGeneration) == "" || runtime.transparentAppliedAt.IsZero() {
		return orchestratorapi.TransparentRuntimeEvidence{}, errors.New("transparent orchestrator has no committed runtime generation")
	}
	observed, err := runtime.adapter.ObserveTransparentOrchestrator(ctx, runtime.transparentRequest+"-status")
	if err != nil {
		return orchestratorapi.TransparentRuntimeEvidence{}, err
	}
	if observed.State != "running" {
		return orchestratorapi.TransparentRuntimeEvidence{}, fmt.Errorf("transparent orchestrator state is %s", observed.State)
	}
	if observed.Generation != runtime.transparentGeneration {
		return orchestratorapi.TransparentRuntimeEvidence{}, fmt.Errorf("transparent orchestrator generation drift: observed %s, committed %s", observed.Generation, runtime.transparentGeneration)
	}
	return orchestratorapi.TransparentRuntimeEvidence{TransactionID: runtime.transparentRequest, Generation: observed.Generation, State: observed.State, AppliedAt: runtime.transparentAppliedAt, ObservedAt: time.Now().UTC()}, nil
}

type transparentStartupRuntime interface {
	ApplyTransparent(context.Context, string, orchestrator.Topology, *orchestrator.Policy) error
}

func reconcileTransparentStartup(ctx context.Context, repository *orchestrator.Repository, runtime transparentStartupRuntime) error {
	topology, _, err := repository.Snapshot(ctx)
	if errors.Is(err, orchestrator.ErrTopologyNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var policy *orchestrator.Policy
	current, _, err := repository.PolicySnapshot(ctx)
	if err == nil {
		policy = &current
	} else if !errors.Is(err, orchestrator.ErrPolicyNotFound) {
		return err
	}
	return runtime.ApplyTransparent(ctx, "orchestrator-startup", topology, policy)
}

func transparentTopologyInterfaces(topology orchestrator.TopologyView) []string {
	seen := map[string]bool{}
	result := []string{}
	appendName := func(name string) {
		name = strings.TrimSpace(name)
		if name != "" && !seen[name] {
			seen[name] = true
			result = append(result, name)
		}
	}
	for _, item := range topology.Interfaces {
		if item.Bond == nil {
			appendName(item.Port)
			continue
		}
		for _, member := range item.Bond.Members {
			appendName(member)
		}
	}
	for _, group := range topology.Groups {
		for _, port := range group.Ports {
			appendName(port.Interface)
		}
	}
	sort.Strings(result)
	return result
}

func (runtime *productionOrchestratorRuntime) ApplyServiceChain(ctx context.Context, requestID string, chain orchestrator.ServiceChain, _ []vpp.NativeAttachment) (vpp.ServiceChainApplyResult, error) {
	if chain.Direct {
		result, err := runtime.adapter.ApplyServiceChain(ctx, requestID, chain, nil)
		runtime.releaseDPDK(ctx, requestID, err)
		return result, err
	}
	interfaces := serviceChainInterfaces(chain)
	path, err := runtime.selectAndPrepare(ctx, requestID, interfaces)
	if err != nil {
		return vpp.ServiceChainApplyResult{}, err
	}
	result, err := runtime.adapter.ApplyServiceChain(ctx, requestID, chain, path.Attachments)
	if err != nil {
		runtime.releaseDPDK(ctx, requestID, err)
	}
	return result, err
}

func (runtime *productionOrchestratorRuntime) ApplyServiceChainTransition(ctx context.Context, requestID string, desired, active orchestrator.ServiceChain, _ []vpp.NativeAttachment) (vpp.ServiceChainApplyResult, error) {
	if active.Direct {
		return runtime.adapter.ApplyServiceChainBypass(ctx, requestID, desired)
	}
	if desired.Direct {
		result, err := runtime.adapter.ApplyServiceChainTransition(ctx, requestID, desired, active, nil)
		runtime.releaseDPDK(ctx, requestID, err)
		return result, err
	}
	interfaces := serviceChainInterfaces(desired)
	path, err := runtime.selectAndPrepare(ctx, requestID, interfaces)
	if err != nil {
		return vpp.ServiceChainApplyResult{}, err
	}
	result, err := runtime.adapter.ApplyServiceChainTransition(ctx, requestID, desired, active, path.Attachments)
	if err != nil {
		runtime.releaseDPDK(ctx, requestID, err)
	}
	return result, err
}

func (runtime *productionOrchestratorRuntime) selectAndPrepare(ctx context.Context, requestID string, interfaces []string) (vpp.NativePath, error) {
	request := vpp.LoadNativePathRequest(runtime.capabilityReport, runtime.managementInterface, interfaces, time.Now().UTC())
	request.RequireSmartQoS = false
	path, err := vpp.SelectNativePath(request)
	if err != nil {
		return vpp.NativePath{}, err
	}
	if path.Tier != vpp.DataplaneTierDPDK {
		return path, nil
	}
	if runtime.dataplane == nil {
		return vpp.NativePath{}, fmt.Errorf("orchestrator DPDK fallback controller is unavailable")
	}
	runtime.dpdkMu.Lock()
	_, active := runtime.dpdkActive[requestID]
	runtime.dpdkMu.Unlock()
	if !active {
		receipt, err := runtime.dataplane.Apply(ctx, dataplane.Request{TransactionID: requestID, Path: path})
		if err != nil {
			return vpp.NativePath{}, fmt.Errorf("orchestrator DPDK fallback apply: %w", err)
		}
		if receipt.Changed {
			runtime.dpdkMu.Lock()
			if runtime.dpdkActive == nil {
				runtime.dpdkActive = map[string]vpp.NativePath{}
			}
			runtime.dpdkActive[requestID] = path
			runtime.dpdkMu.Unlock()
		}
	}
	return path, nil
}

func (runtime *productionOrchestratorRuntime) releaseDPDK(ctx context.Context, requestID string, cause error) {
	runtime.dpdkMu.Lock()
	_, active := runtime.dpdkActive[requestID]
	if active {
		delete(runtime.dpdkActive, requestID)
	}
	runtime.dpdkMu.Unlock()
	if active && runtime.dataplane != nil {
		if _, err := runtime.dataplane.Rollback(ctx, requestID); err != nil && cause == nil {
			log.Printf("orchestrator DPDK rollback failed for %s: %v", requestID, err)
		}
	}
}

func (runtime *productionOrchestratorRuntime) ServiceChainUnavailable(ctx context.Context, bindings []orchestrator.ServiceChainBindingInput) (map[string]bool, []orchestrator.GroupHealth, error) {
	unavailable, health := runtime.tracker.Evaluate(ctx, bindings, runtime.probe)
	return unavailable, health, nil
}

type icmpHealthProbe struct{ binary string }

func (probe icmpHealthProbe) Reachable(ctx context.Context, address string) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	return exec.CommandContext(probeCtx, probe.binary, "-n", "-c", "1", "-W", "1", strings.TrimSpace(address)).Run() == nil
}

func serviceChainInterfaces(chain orchestrator.ServiceChain) []string {
	seen := map[string]bool{}
	interfaces := []string{}
	appendInterface := func(name string) {
		name = strings.TrimSpace(name)
		if name != "" && !seen[name] {
			seen[name] = true
			interfaces = append(interfaces, name)
		}
	}
	for _, path := range []orchestrator.ServiceChainPath{chain.Forward, chain.Reverse} {
		appendInterface(path.IngressInterface)
		appendInterface(path.ExitInterface)
		for _, hop := range path.Hops {
			appendInterface(hop.IngressInterface)
			appendInterface(hop.ServiceInterface)
			appendInterface(hop.ReturnInterface)
		}
	}
	sort.Strings(interfaces)
	return interfaces
}
*/

func loadProductProfile(buildID, manifestPath string) (product.Profile, error) {
	path := strings.TrimSpace(manifestPath)
	if path == "" {
		return product.ParseProfile(buildID)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return product.Profile{}, fmt.Errorf("read product manifest %s: %w", path, err)
	}
	manifest, err := product.ParseManifest(data)
	if err != nil {
		return product.Profile{}, fmt.Errorf("parse product manifest %s: %w", path, err)
	}
	return manifest.Profile(), nil
}

type statePaths struct {
	Database string
	Config   string
}

func defaultStatePaths(profile product.Profile, exists func(string) bool) statePaths {
	if profile.ID() == product.Gateway().ID() {
		paths := statePaths{Database: "/var/lib/ly-route/gateway/ly-route.db", Config: "/var/lib/ly-route/gateway/config.json"}
		if exists("/var/lib/ly-route/ly-route.db") {
			paths.Database = "/var/lib/ly-route/ly-route.db"
		}
		if exists("/var/lib/ly-route/config.json") {
			paths.Config = "/var/lib/ly-route/config.json"
		}
		return paths
	}
	return statePaths{Database: "/var/lib/ly-route/orchestrator/ly-route.db", Config: "/var/lib/ly-route/orchestrator/config.json"}
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func openStore(ctx context.Context, path string, productID product.ID) (*persistence.Store, error) {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0750); err != nil {
			return nil, err
		}
	}
	return persistence.OpenForProduct(ctx, path, productID)
}

func env(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func envBool(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if value == "" {
		return fallback
	}
	return value == "1" || value == "true" || value == "yes" || value == "on"
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

type vppctlInterfaceTelemetry struct {
	binary string
}

func (collector vppctlInterfaceTelemetry) Interfaces(ctx context.Context) ([]map[string]any, error) {
	binary := strings.TrimSpace(collector.binary)
	if binary == "" {
		binary = "vppctl"
	}
	output, err := exec.CommandContext(ctx, binary, "show", "interface").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("vppctl show interface failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return parseVPPInterfaceTelemetry(string(output)), nil
}

func parseVPPInterfaceTelemetry(output string) []map[string]any {
	var items []map[string]any
	var current map[string]any
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if strings.HasPrefix(fields[0], "lyroute-") {
			linuxName := strings.TrimPrefix(fields[0], "lyroute-")
			current = map[string]any{
				"id":            linuxName,
				"name":          linuxName,
				"vpp_interface": fields[0],
				"active_path":   "af_xdp",
				"work_mode":     "af_xdp",
				"admin_state":   "up",
				"runtime_state": "running",
				"rx_bps":        0,
				"tx_bps":        0,
				"rx_pps":        0,
				"tx_pps":        0,
				"rx_bytes":      0,
				"tx_bytes":      0,
				"rx_packets":    0,
				"tx_packets":    0,
				"sessions":      0,
			}
			if len(fields) > 2 {
				current["link_state"] = fields[2]
			}
			items = append(items, current)
		}
		if current != nil {
			applyVPPCounterLine(current, line)
		}
	}
	return items
}

func applyVPPCounterLine(item map[string]any, line string) {
	counters := map[string]string{
		"rx packets": "rx_packets",
		"tx packets": "tx_packets",
		"rx bytes":   "rx_bytes",
		"tx bytes":   "tx_bytes",
	}
	text := strings.ToLower(line)
	for label, key := range counters {
		if !strings.Contains(text, label) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		value, err := strconv.ParseInt(fields[len(fields)-1], 10, 64)
		if err == nil {
			item[key] = value
		}
	}
}
