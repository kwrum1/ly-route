package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	controlapi "ly-route/backend/internal/api"
	"ly-route/backend/internal/geodata"
	"ly-route/backend/internal/orchestrator"
	"ly-route/backend/internal/orchestratorapi"
	"ly-route/backend/internal/persistence"
	"ly-route/backend/internal/product"
	"ly-route/backend/internal/runtime/apply"
	"ly-route/backend/internal/runtime/dns"
	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/nat"
	"ly-route/backend/internal/runtime/proxy"
	serviceRuntime "ly-route/backend/internal/runtime/service"
	"ly-route/backend/internal/runtime/trafficpolicy"
	"ly-route/backend/internal/runtime/vpp"
)

const (
	DefaultVersion     = "dev"
	HeaderRequestID    = "X-Request-ID"
	HeaderDNSSyncToken = "X-LY-Route-DNS-Sync-Token"
)

type HealthResponse struct {
	Status       string                       `json:"status"`
	Version      string                       `json:"version"`
	Degraded     bool                         `json:"degraded"`
	RequestID    string                       `json:"request_id"`
	Dependencies []controlapi.CapabilityState `json:"dependencies"`
}

type ModeResponse struct {
	Mode                   string   `json:"mode"`
	Initialized            bool     `json:"initialized"`
	Switchable             bool     `json:"switchable"`
	PreserveAdminAccount   bool     `json:"preserve_admin_account"`
	PreserveManagementPort bool     `json:"preserve_management_port"`
	Capabilities           []string `json:"capabilities"`
	RequestID              string   `json:"request_id"`
}

type ModeInitializeRequest struct {
	Mode                   string `json:"mode"`
	ConfirmReset           bool   `json:"confirm_reset"`
	PreserveAdminAccount   bool   `json:"preserve_admin_account"`
	PreserveManagementPort bool   `json:"preserve_management_port"`
}

type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

type ErrorBody struct {
	Code       string             `json:"code"`
	Message    string             `json:"message"`
	RequestID  string             `json:"request_id"`
	Capability product.Capability `json:"capability,omitempty"`
}

type DNSPolicyResource struct {
	ID           string                       `json:"id"`
	Kind         string                       `json:"kind"`
	Name         string                       `json:"name"`
	Priority     int                          `json:"priority"`
	Enabled      bool                         `json:"enabled"`
	Policy       dns.Policy                   `json:"policy"`
	Render       dns.SmartDNSRender           `json:"render"`
	Capabilities []controlapi.CapabilityState `json:"capabilities,omitempty"`
}

type DNSRuleUpdateRequest struct {
	PolicyID       string     `json:"policy_id"`
	ExpectedSHA256 string     `json:"expected_sha256"`
	Rules          []dns.Rule `json:"rules"`
}

type DNSIPSetObservationRequest struct {
	RuleID  string            `json:"rule_id"`
	SetName string            `json:"set_name"`
	Members []dns.IPSetMember `json:"members"`
}

type ProxyEgressWriteRequest struct {
	ID             string             `json:"id"`
	Kind           string             `json:"kind"`
	Name           string             `json:"name"`
	Enabled        bool               `json:"enabled"`
	SemanticType   proxy.SemanticType `json:"semantic_type"`
	DisplayList    string             `json:"display_list"`
	ProxyProfileID string             `json:"proxy_profile_id"`
	UnderlayWANID  string             `json:"underlay_wan_id"`
	NodeID         string             `json:"node_id,omitempty"`
	SubscriptionID string             `json:"subscription_id,omitempty"`
	LowCopy        bool               `json:"low_copy"`
	Description    string             `json:"description,omitempty"`
}

type ConfigApplyRequest struct {
	ProxyEgress *proxy.Egress `json:"proxy_egress,omitempty"`
	FlowIntent  *flow.Intent  `json:"flow_intent,omitempty"`
}

type ConfigSnapshotRequest struct {
	Name string `json:"name"`
}

type Server struct {
	profile                       product.Profile
	version                       string
	proxyEgress                   proxy.Egress
	flowIntent                    flow.Intent
	runtimeProxyConfigured        bool
	runtimeFlowConfigured         bool
	auth                          AuthConfig
	sessions                      *sessionStore
	now                           func() time.Time
	auditMu                       sync.Mutex
	audit                         []AuditEvent
	store                         *persistence.Store
	services                      *serviceRuntime.Runtime
	vppReceiptPath                string
	gatewayTransaction            apply.GatewayTransactionRunner
	orchestratorRepository        orchestrator.Orchestrator
	orchestratorRuntime           orchestratorapi.ServiceChainRuntime
	orchestratorReconcileInterval time.Duration
	orchestratorTelemetry         OrchestratorTelemetryCollector
	interfaceTelemetry            InterfaceTelemetryCollector
	dhcpLeases                    DHCPLeaseCollector
	vppCounters                   VPPCounterCollector
	policyHits                    PolicyHitCollector
	topTelemetry                  TopTelemetryCollector
	subscriptionFetch             func(context.Context, string, bool) ([]byte, error)
	trafficTrend                  TrafficTrendCollector
	gatewayTelemetry              GatewayTelemetryCollector
	gatewayState                  *gatewayTelemetryState
	smartQoSObserver              SmartQoSRuntimeObserver
	securityGuardObserver         SecurityGuardRuntimeObserver
	requireSmartQoS               bool
	runtimeMu                     sync.Mutex
	runtimeApplyMu                sync.Mutex
	lastRuntime                   *RuntimeApplyResult
	firmwareMu                    sync.Mutex
	firmwareStageDir              string
	firmwareStatus                FirmwareUpdateStatus
	firmwareInstallStart          func(firmwareInstallInvocation) error
	dnsSyncToken                  string
}

type InterfaceTelemetryCollector interface {
	Interfaces(context.Context) ([]map[string]any, error)
}

type DHCPLeaseCollector interface {
	Leases(context.Context) ([]map[string]any, error)
}

type VPPCounterCollector interface {
	Dashboard(context.Context) (map[string]any, error)
	PolicyHits(context.Context) ([]map[string]any, error)
}

type PolicyHitCollector interface {
	PolicyHits(context.Context) ([]map[string]any, error)
}

type TopTelemetryCollector interface {
	TopSessions(context.Context) ([]map[string]any, error)
	TopDomains(context.Context) ([]map[string]any, error)
}

type RuntimeComponentState struct {
	Name          string             `json:"name"`
	State         string             `json:"state"`
	Available     bool               `json:"available"`
	TransactionID string             `json:"transaction_id"`
	ApplyReceipt  apply.ApplyReceipt `json:"apply_receipt"`
	ReadbackAt    time.Time          `json:"readback_at"`
	Fresh         bool               `json:"fresh"`
	Capability    string             `json:"affected_capability"`
	Reason        string             `json:"reason"`
}

type RuntimePlan struct {
	ProxyEgress        proxy.Egress                       `json:"proxy_egress"`
	CompiledProxy      proxy.CompiledEgress               `json:"compiled_proxy"`
	FlowIntent         flow.Intent                        `json:"flow_intent"`
	CompiledFlow       flow.CompiledIntent                `json:"compiled_flow"`
	CompiledNAT        nat.CompiledConfig                 `json:"compiled_nat"`
	CompiledPolicy     trafficpolicy.Config               `json:"compiled_policy"`
	DNSPolicies        []DNSPolicyResource                `json:"dns_policies"`
	ServiceArtifacts   []RuntimeArtifactSummary           `json:"service_artifacts"`
	RuntimeArtifacts   []serviceRuntime.RenderedArtifact  `json:"-"`
	VppOperations      []vpp.Operation                    `json:"vpp_operations"`
	NftablesCapture    proxy.NftablesCapturePlan          `json:"nftables_tproxy_plan"`
	DNSInterception    serviceRuntime.DNSInterceptionPlan `json:"dns_interception_plan"`
	LinuxPolicyRouting proxy.LinuxPolicyRoutingPlan       `json:"linux_policy_routing_plan"`
	DHCPServers        []serviceRuntime.KeaDHCP4Plan      `json:"dhcp_servers,omitempty"`
	PPPoEPeers         []RuntimePPPoEPeerSummary          `json:"pppoe_peers,omitempty"`
	Components         []RuntimeComponentState            `json:"components"`
	Warnings           []string                           `json:"warnings,omitempty"`
	DataplaneState     string                             `json:"dataplane_state"`
	DataplaneProof     []vpp.PrerequisiteResult           `json:"dataplane_prerequisites,omitempty"`
	GatewayPlan        vpp.Plan                           `json:"-"`
}

type RuntimeArtifactSummary struct {
	Service     serviceRuntime.ServiceName `json:"service"`
	Path        string                     `json:"path"`
	ContentHash string                     `json:"content_hash"`
	ReloadMode  string                     `json:"reload_mode"`
}

type RuntimePPPoEPeerSummary struct {
	ID        string `json:"id"`
	Interface string `json:"interface"`
	Username  string `json:"username"`
	MTU       int    `json:"mtu,omitempty"`
	MRU       int    `json:"mru,omitempty"`
}

type RuntimeApplyResult struct {
	Status                string                            `json:"status"`
	RuntimeState          string                            `json:"runtime_state"`
	TransactionID         string                            `json:"transaction_id"`
	Reason                string                            `json:"reason,omitempty"`
	Components            []RuntimeComponentState           `json:"components"`
	Applied               []string                          `json:"applied_services,omitempty"`
	SnapshotHash          string                            `json:"snapshot_hash,omitempty"`
	AppliedAt             time.Time                         `json:"applied_at"`
	Receipt               apply.ApplyReceipt                `json:"apply_receipt"`
	Readback              apply.Readback                    `json:"readback"`
	GatewayPlan           *vpp.Plan                         `json:"-"`
	GatewayEvidence       []apply.GatewayResourceEvidence   `json:"gateway_evidence,omitempty"`
	RollbackReceipt       apply.RollbackReceipt             `json:"rollback_receipt,omitempty"`
	ReconciliationReceipt apply.ReconciliationReceipt       `json:"reconciliation_receipt,omitempty"`
	CapabilityFailures    []apply.CapabilityFailureEvidence `json:"capability_failures,omitempty"`
}

type FirmwareUpdateStatus struct {
	Staged           bool      `json:"staged"`
	Installing       bool      `json:"installing,omitempty"`
	InstallStatus    string    `json:"install_status,omitempty"`
	ImagePath        string    `json:"image_path,omitempty"`
	ChecksumPath     string    `json:"checksum_path,omitempty"`
	ImageHash        string    `json:"image_hash,omitempty"`
	ImageSize        int64     `json:"image_size,omitempty"`
	ConfigBackupPath string    `json:"config_backup_path,omitempty"`
	ConfigBackupHash string    `json:"config_backup_hash,omitempty"`
	ConfigBackupSize int64     `json:"config_backup_size,omitempty"`
	StagedAt         time.Time `json:"staged_at,omitempty"`
	InstallCommand   string    `json:"install_command,omitempty"`
	Reason           string    `json:"reason,omitempty"`
}

type firmwareInstallRequest struct {
	ConfirmInstall bool   `json:"confirm_install"`
	TargetDir      string `json:"target_dir"`
	Reboot         bool   `json:"reboot"`
}

type Option func(*Server)

func WithVersion(version string) Option {
	return func(server *Server) {
		if strings.TrimSpace(version) != "" {
			server.version = version
		}
	}
}

func WithProxyEgress(egress proxy.Egress) Option {
	return func(server *Server) {
		server.proxyEgress = egress
		server.runtimeProxyConfigured = true
	}
}

func WithFlowIntent(intent flow.Intent) Option {
	return func(server *Server) {
		server.flowIntent = intent
		server.runtimeFlowConfigured = true
	}
}

func WithAuthConfig(config AuthConfig) Option {
	return func(server *Server) { server.auth = config }
}

func WithDNSSyncToken(token string) Option {
	return func(server *Server) { server.dnsSyncToken = strings.TrimSpace(token) }
}

func WithClock(now func() time.Time) Option {
	return func(server *Server) {
		if now != nil {
			server.now = now
			server.sessions.now = now
		}
	}
}

func WithStore(store *persistence.Store) Option {
	return func(server *Server) { server.store = store }
}

func WithSubscriptionFetcher(fetch func(context.Context, string, bool) ([]byte, error)) Option {
	return func(server *Server) {
		if fetch != nil {
			server.subscriptionFetch = fetch
		}
	}
}

func WithFirmwareInstallStart(start func(firmwareInstallInvocation) error) Option {
	return func(server *Server) { server.firmwareInstallStart = start }
}

func WithServiceRuntime(runtime serviceRuntime.Runtime) Option {
	return func(server *Server) { server.services = &runtime }
}

func WithVPPReceiptPath(path string) Option {
	return func(server *Server) { server.vppReceiptPath = strings.TrimSpace(path) }
}

func WithGatewayTransaction(transaction apply.GatewayTransactionRunner) Option {
	return func(server *Server) { server.gatewayTransaction = transaction }
}

func WithSmartQoSRuntime(observer SmartQoSRuntimeObserver, required bool) Option {
	return func(server *Server) {
		server.smartQoSObserver = observer
		server.requireSmartQoS = required
	}
}

func WithSecurityGuardRuntime(observer SecurityGuardRuntimeObserver) Option {
	return func(server *Server) { server.securityGuardObserver = observer }
}

func WithOrchestratorRepository(repository orchestrator.Orchestrator) Option {
	return func(server *Server) { server.orchestratorRepository = repository }
}

func WithOrchestratorRuntime(runtime orchestratorapi.ServiceChainRuntime) Option {
	return func(server *Server) { server.orchestratorRuntime = runtime }
}

func WithOrchestratorReconcileInterval(interval time.Duration) Option {
	return func(server *Server) { server.orchestratorReconcileInterval = interval }
}

func WithInterfaceTelemetry(collector InterfaceTelemetryCollector) Option {
	return func(server *Server) { server.interfaceTelemetry = collector }
}

func WithDHCPLeases(collector DHCPLeaseCollector) Option {
	return func(server *Server) { server.dhcpLeases = collector }
}

func WithVPPCounters(collector VPPCounterCollector) Option {
	return func(server *Server) { server.vppCounters = collector }
}

func WithPolicyHitTelemetry(collector PolicyHitCollector) Option {
	return func(server *Server) { server.policyHits = collector }
}

func WithTopTelemetry(collector TopTelemetryCollector) Option {
	return func(server *Server) { server.topTelemetry = collector }
}

func WithTrafficTrend(collector TrafficTrendCollector) Option {
	return func(server *Server) { server.trafficTrend = collector }
}

func WithGatewayTelemetry(collector GatewayTelemetryCollector) Option {
	return func(server *Server) { server.gatewayTelemetry = collector }
}

func WithFirmwareStageDir(path string) Option {
	return func(server *Server) {
		if strings.TrimSpace(path) != "" {
			server.firmwareStageDir = path
		}
	}
}

func New(options ...Option) *Server {
	return newServer(product.Gateway(), options...)
}

func NewServer(selection *product.Selection, options ...Option) (*Server, error) {
	if selection == nil {
		return nil, fmt.Errorf("new server: %w", product.ErrSelectionUninitialized)
	}
	profile, err := selection.Profile()
	if err != nil {
		return nil, fmt.Errorf("new server: %w", err)
	}
	server := newServer(profile, options...)
	if server.store != nil && server.store.ProductID() != profile.ID() {
		return nil, &persistence.ProductMismatchError{Expected: profile.ID(), Actual: server.store.ProductID()}
	}
	return server, nil
}

func newServer(profile product.Profile, options ...Option) *Server {
	server := &Server{
		profile:          profile,
		version:          DefaultVersion,
		vppReceiptPath:   envOrDefault("LY_ROUTE_VPP_RECEIPT", "/var/lib/ly-route/vpp-apply-receipt.json"),
		firmwareStageDir: envOrDefault("LY_ROUTE_FIRMWARE_STAGE_DIR", "/var/lib/ly-route/firmware-update"),
		firmwareInstallStart: func(invocation firmwareInstallInvocation) error {
			return exec.Command(invocation.Name, invocation.Args...).Start()
		},
		subscriptionFetch: proxy.FetchSubscription,
		dnsSyncToken:      loadDNSSyncToken(),
		proxyEgress:       proxy.NewProxyEgress("proxy-egress-default", "xray-tproxy-outbound"),
		flowIntent: flow.NewIntent("default", []flow.Rule{
			flow.NewRule("classify-default", flow.RuleGranularity, flow.Classify("best-effort")),
		}),
		auth:         defaultAuthConfig(),
		now:          time.Now,
		sessions:     newSessionStore(time.Now),
		gatewayState: newGatewayTelemetryState(),
	}
	for _, option := range options {
		option(server)
	}
	server.reconcileRuntime(context.Background())
	return server
}

func (server *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	if server.profile.ID() == product.Orchestrator().ID() && server.orchestratorRepository != nil {
		handler, err := orchestratorapi.New(server.orchestratorRepository, orchestratorSessionAccess{server: server}, server.orchestratorRuntime)
		if err != nil {
			panic(fmt.Sprintf("compose orchestrator API: %v", err))
		}
		handler.SetManagementSharingResolver(func(ctx context.Context, interfaceID string) bool {
			item := server.managementNetworkState(ctx, false)
			return normalizeManagementMode(stringField(item, "mode")) == "shared_lan" && stringField(item, "interface_id") == interfaceID
		})
		handler.RegisterRoutes(mux)
		if _, transparent := server.orchestratorRuntime.(orchestratorapi.TransparentRuntime); !transparent {
			handler.StartServiceChainReconciler(context.Background(), server.orchestratorReconcileInterval)
		}
	}
	for _, route := range productRoutes() {
		if server.profile.Allows(route.capability) {
			mux.HandleFunc(route.pattern, route.handler(server))
		}
	}
	mux.HandleFunc("/api/v1", server.handleNotFound)
	mux.HandleFunc("/api/v1/", server.handleNotFound)
	return requestIDMiddleware(mux)
}

type orchestratorSessionAccess struct{ server *Server }

func (access orchestratorSessionAccess) Authorize(request *http.Request, permission orchestratorapi.Permission) error {
	if access.server == nil {
		return orchestratorapi.ErrAuthenticationRequired
	}
	session, ok := access.server.sessionFromRequest(request)
	if !ok {
		return orchestratorapi.ErrAuthenticationRequired
	}
	if permission == orchestratorapi.PermissionAdminWrite && (session.Role != "admin" || session.PasswordChangeRequired) {
		return orchestratorapi.ErrAdminRequired
	}
	return nil
}

func (server *Server) handleDNSPolicies(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if server.store == nil {
			writeError(w, r, http.StatusServiceUnavailable, "store_unavailable", "local store is not configured")
			return
		}
		documents, err := server.store.Policies(r.Context(), "dns-policy")
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "dns_policy_read_failed", "DNS policies are unavailable")
			return
		}
		items := make([]DNSPolicyResource, 0, len(documents))
		for _, document := range documents {
			resource, err := server.dnsPolicyResource(r.Context(), document.PolicyID, document.PolicyID, document.Enabled, document.Payload)
			if err != nil {
				writeError(w, r, http.StatusInternalServerError, "dns_policy_invalid", err.Error())
				return
			}
			resource.Priority = normalizedDNSPolicyPriority(document.Priority)
			items = append(items, resource)
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "request_id": requestID(r)})
	case http.MethodPost:
		session, ok := server.sessionFromRequest(r)
		if !ok {
			server.recordAudit("anonymous", "system", "/api/v1/dns/policies", "create", "denied", "authentication required", r)
			writeError(w, r, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		if session.Role != "admin" {
			server.recordAudit(session.Username, session.Role, "/api/v1/dns/policies", "create", "denied", "readonly mutation denied", r)
			writeError(w, r, http.StatusForbidden, "forbidden", "admin role required")
			return
		}
		if server.passwordChangeRequired(w, r, session, "/api/v1/dns/policies", "create") {
			return
		}
		if server.store == nil {
			writeError(w, r, http.StatusServiceUnavailable, "store_unavailable", "local store is not configured")
			return
		}
		var req DNSPolicyResource
		if err := decodeStrictJSON(r, &req); err != nil {
			server.recordAudit(session.Username, session.Role, "/api/v1/dns/policies", "create", "failure", err.Error(), r)
			writeError(w, r, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
			return
		}
		resource, payload, hash, err := server.compileDNSPolicyResource(r.Context(), req)
		if err != nil {
			server.recordAudit(session.Username, session.Role, "/api/v1/dns/policies", "create", "failure", err.Error(), r)
			writeError(w, r, http.StatusUnprocessableEntity, "invalid_dns_policy", err.Error())
			return
		}
		if err := server.store.SavePolicy(r.Context(), persistence.PolicyDocument{Namespace: "dns-policy", PolicyID: resource.ID, Priority: resource.Priority, Enabled: resource.Enabled, Payload: payload, PayloadHash: hash, UpdatedAt: server.now().UTC()}); err != nil {
			server.recordAudit(session.Username, session.Role, "/api/v1/dns/policies", "create", "failure", err.Error(), r)
			writeError(w, r, http.StatusInternalServerError, "dns_policy_save_failed", "DNS policy could not be saved")
			return
		}
		server.recordAudit(session.Username, session.Role, "/api/v1/dns/policies", "create", "success", "", r)
		writeJSON(w, http.StatusOK, map[string]any{"item": resource, "request_id": requestID(r)})
	default:
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
	}
}

func (server *Server) handleDNSPolicyItem(w http.ResponseWriter, r *http.Request) {
	id, action := splitPathRemainder("/api/v1/dns/policies/", r.URL.Path)
	if id == "" || action != "" {
		writeError(w, r, http.StatusNotFound, "not_found", "resource was not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		if server.store == nil {
			writeError(w, r, http.StatusServiceUnavailable, "store_unavailable", "local store is not configured")
			return
		}
		document, err := server.store.Policy(r.Context(), "dns-policy", id)
		if err != nil {
			if errors.Is(err, persistence.ErrNotFound) {
				if id == "default" {
					resource, err := server.defaultDNSPolicyResource(r.Context())
					if err != nil {
						writeError(w, r, http.StatusInternalServerError, "dns_policy_invalid", err.Error())
						return
					}
					writeJSON(w, http.StatusOK, map[string]any{"item": resource, "request_id": requestID(r)})
					return
				}
				writeError(w, r, http.StatusNotFound, "not_found", "resource was not found")
				return
			}
			writeError(w, r, http.StatusInternalServerError, "dns_policy_read_failed", "DNS policy is unavailable")
			return
		}
		resource, err := server.dnsPolicyResource(r.Context(), document.PolicyID, document.PolicyID, document.Enabled, document.Payload)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "dns_policy_invalid", err.Error())
			return
		}
		resource.Priority = normalizedDNSPolicyPriority(document.Priority)
		writeJSON(w, http.StatusOK, map[string]any{"item": resource, "request_id": requestID(r)})
	case http.MethodPost, http.MethodPatch:
		server.handleDNSPolicyMutation(w, r, id)
	case http.MethodDelete:
		server.handleDNSPolicyDelete(w, r, id)
	default:
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
	}
}

func (server *Server) handleDNSResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	domain := strings.TrimSpace(r.URL.Query().Get("domain"))
	if domain == "" {
		writeError(w, r, http.StatusBadRequest, "domain_required", "domain query parameter is required")
		return
	}
	resource, err := server.activeDNSPolicyResource(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "dns_policy_invalid", err.Error())
		return
	}
	// The persisted policy keeps object-group references and UI aliases such as
	// source "any" so the control-plane representation remains editable.  The
	// decision endpoint must compile the same expanded policy used by runtime
	// rendering; compiling the raw payload here makes a valid UI policy fail
	// closed with an invalid-source error.
	expandedPolicy, err := server.expandDNSPolicyForDecision(r.Context(), resource.Policy)
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "dns_policy_compile_failed", err.Error())
		return
	}
	compiled, err := dns.CompilePolicy(expandedPolicy, []proxy.Egress{server.proxyEgress})
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "dns_policy_compile_failed", err.Error())
		return
	}
	unavailable := map[string]bool{}
	for _, resolver := range strings.Split(r.URL.Query().Get("unavailable_resolvers"), ",") {
		resolver = strings.TrimSpace(resolver)
		if resolver != "" {
			unavailable[resolver] = true
		}
	}
	decision := dns.DecideForSource(compiled, domain, strings.TrimSpace(r.URL.Query().Get("source_ip")), unavailable)
	writeJSON(w, http.StatusOK, map[string]any{"policy_id": resource.ID, "decision": decision, "request_id": requestID(r)})
}

func (server *Server) handleDNSRuleUpdates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	session, ok := server.sessionFromRequest(r)
	if !ok {
		server.recordAudit("anonymous", "system", "/api/v1/dns/rule-updates", "update", "denied", "authentication required", r)
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	if session.Role != "admin" {
		server.recordAudit(session.Username, session.Role, "/api/v1/dns/rule-updates", "update", "denied", "readonly mutation denied", r)
		writeError(w, r, http.StatusForbidden, "forbidden", "admin role required")
		return
	}
	if server.passwordChangeRequired(w, r, session, "/api/v1/dns/rule-updates", "update") {
		return
	}
	if server.store == nil {
		writeError(w, r, http.StatusServiceUnavailable, "store_unavailable", "local store is not configured")
		return
	}
	var req DNSRuleUpdateRequest
	if err := decodeStrictJSON(r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
		return
	}
	policyID := strings.TrimSpace(nonEmpty(req.PolicyID, "default"))
	rulesPayload, rulesHash, err := persistence.MarshalPayload(req.Rules)
	if err != nil {
		server.recordAudit(session.Username, session.Role, "/api/v1/dns/rule-updates", "update", "failure", err.Error(), r)
		writeError(w, r, http.StatusBadRequest, "invalid_rules", "rules could not be encoded")
		return
	}
	if strings.TrimSpace(req.ExpectedSHA256) != rulesHash {
		server.recordAudit(session.Username, session.Role, "/api/v1/dns/rule-updates", "update", "failure", "checksum mismatch", r)
		writeError(w, r, http.StatusUnprocessableEntity, "checksum_mismatch", "rule update checksum does not match payload")
		return
	}
	previous, err := server.dnsPolicyResourceForID(r.Context(), policyID)
	if err != nil {
		server.recordAudit(session.Username, session.Role, "/api/v1/dns/rule-updates", "update", "failure", err.Error(), r)
		writeError(w, r, http.StatusInternalServerError, "dns_policy_read_failed", err.Error())
		return
	}
	nextPolicy := previous.Policy
	nextPolicy.Rules = append([]dns.Rule(nil), req.Rules...)
	resource, payload, payloadHash, err := server.compileDNSPolicyResource(r.Context(), DNSPolicyResource{ID: policyID, Kind: "policy", Name: nonEmpty(previous.Name, policyID), Enabled: true, Policy: nextPolicy})
	if err != nil {
		server.recordAudit(session.Username, session.Role, "/api/v1/dns/rule-updates", "update", "failure", err.Error(), r)
		writeError(w, r, http.StatusUnprocessableEntity, "invalid_dns_policy", err.Error())
		return
	}
	rollbackPayload, rollbackHash, err := persistence.MarshalPayload(map[string]any{"policy_id": previous.ID, "policy": previous.Policy, "rules_sha256": rulesHash, "rules_payload": json.RawMessage(rulesPayload), "retained_at": server.now().UTC()})
	if err != nil {
		server.recordAudit(session.Username, session.Role, "/api/v1/dns/rule-updates", "update", "failure", err.Error(), r)
		writeError(w, r, http.StatusInternalServerError, "rollback_encode_failed", "rollback metadata could not be encoded")
		return
	}
	if err := server.store.SaveConfig(r.Context(), persistence.ConfigDocument{ResourceType: "dns_rule_update_rollback", ResourceID: policyID, Payload: rollbackPayload, PayloadHash: rollbackHash, UpdatedAt: server.now().UTC()}); err != nil {
		server.recordAudit(session.Username, session.Role, "/api/v1/dns/rule-updates", "update", "failure", err.Error(), r)
		writeError(w, r, http.StatusInternalServerError, "rollback_save_failed", "rollback metadata could not be saved")
		return
	}
	if err := server.store.SavePolicy(r.Context(), persistence.PolicyDocument{Namespace: "dns-policy", PolicyID: resource.ID, Priority: resource.Priority, Enabled: resource.Enabled, Payload: payload, PayloadHash: payloadHash, UpdatedAt: server.now().UTC()}); err != nil {
		server.recordAudit(session.Username, session.Role, "/api/v1/dns/rule-updates", "update", "failure", err.Error(), r)
		writeError(w, r, http.StatusInternalServerError, "dns_policy_save_failed", "DNS policy could not be saved")
		return
	}
	server.recordAudit(session.Username, session.Role, "/api/v1/dns/rule-updates", "update", "success", "", r)
	writeJSON(w, http.StatusOK, map[string]any{"item": resource, "rules_sha256": rulesHash, "rollback_retained": true, "request_id": requestID(r)})
}

func (server *Server) handleDNSIPSetObservations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	if !server.validDNSSyncRequest(r) {
		writeError(w, r, http.StatusUnauthorized, "dns_sync_unauthorized", "DNS observation endpoint requires a local sync token")
		return
	}
	if server.store == nil {
		writeError(w, r, http.StatusServiceUnavailable, "store_unavailable", "local store is not configured")
		return
	}
	var req DNSIPSetObservationRequest
	if err := decodeStrictJSON(r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
		return
	}
	req.RuleID = strings.TrimSpace(req.RuleID)
	req.SetName = strings.TrimSpace(req.SetName)
	policy, err := server.activeDNSPolicyResource(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "dns_policy_read_failed", "active DNS policy is unavailable")
		return
	}
	var rule *dns.Rule
	for index := range policy.Policy.Rules {
		candidate := &policy.Policy.Rules[index]
		if candidate.ID == req.RuleID {
			rule = candidate
			break
		}
	}
	if rule == nil || rule.Outcome.Kind != dns.OutcomeDirect || strings.TrimSpace(rule.Outcome.WANEgressID) == "" {
		writeError(w, r, http.StatusUnprocessableEntity, "dns_sync_rule_invalid", "observation rule must be an enabled direct fixed-WAN DNS rule")
		return
	}
	expectedSet := dns.SmartDNSIPSetName(req.RuleID)
	if req.SetName != expectedSet {
		writeError(w, r, http.StatusUnprocessableEntity, "dns_sync_set_mismatch", "observation set does not match the DNS rule")
		return
	}
	now := server.now().UTC()
	validMembers := make([]dns.IPSetMember, 0, len(req.Members))
	seen := map[string]struct{}{}
	for _, member := range req.Members {
		if member.SetName != expectedSet || !member.ExpiresAt.After(now) {
			continue
		}
		if _, err := netip.ParseAddr(member.IP); err != nil {
			writeError(w, r, http.StatusUnprocessableEntity, "dns_sync_address_invalid", "observation contains an invalid IP address")
			return
		}
		member.IP = netip.MustParseAddr(member.IP).String()
		if _, exists := seen[member.IP]; exists {
			continue
		}
		seen[member.IP] = struct{}{}
		validMembers = append(validMembers, member)
	}
	items, err := server.desiredItems(r.Context(), "domain_ip_set")
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "dns_sync_read_failed", "existing DNS observations are unavailable")
		return
	}
	prefix := "dns-observed-" + req.RuleID + "-"
	// The sync timer refreshes member expiry timestamps frequently.  Those
	// timestamps are persistence data, but they do not change the VPP route
	// set.  Re-applying the complete runtime for every timestamp refresh can
	// serialize indefinitely behind runtimeApplyMu and starve the control API.
	// Only a change in the observed address set requires a dataplane apply.
	existingIPs := map[string]struct{}{}
	for _, item := range items {
		id := stringField(item, "id")
		if strings.HasPrefix(id, prefix) {
			for _, ip := range stringSliceField(item, "ips") {
				if address, parseErr := netip.ParseAddr(strings.TrimSpace(ip)); parseErr == nil {
					existingIPs[address.String()] = struct{}{}
				}
			}
			if err := server.store.DeleteConfig(r.Context(), "domain_ip_set", id); err != nil && !errors.Is(err, persistence.ErrNotFound) {
				writeError(w, r, http.StatusInternalServerError, "dns_sync_delete_failed", "stale DNS observation could not be removed")
				return
			}
		}
	}
	observedIPs := make(map[string]struct{}, len(validMembers))
	for _, member := range validMembers {
		observedIPs[member.IP] = struct{}{}
		item := map[string]any{
			"id":          prefix + strings.ReplaceAll(member.IP, ":", "_"),
			"kind":        "observed",
			"enabled":     true,
			"dns_rule_id": req.RuleID,
			"ips":         []string{member.IP},
			"expires_at":  member.ExpiresAt.UTC().Format(time.RFC3339),
		}
		payload, hash, marshalErr := persistence.MarshalPayload(item)
		if marshalErr != nil {
			writeError(w, r, http.StatusInternalServerError, "dns_sync_encode_failed", "DNS observation could not be encoded")
			return
		}
		if saveErr := server.store.SaveConfig(r.Context(), persistence.ConfigDocument{ResourceType: "domain_ip_set", ResourceID: item["id"].(string), Payload: payload, PayloadHash: hash, UpdatedAt: now}); saveErr != nil {
			writeError(w, r, http.StatusInternalServerError, "dns_sync_save_failed", "DNS observation could not be saved")
			return
		}
	}
	runtimeApply := json.RawMessage(`{"status":"unchanged"}`)
	if !sameStringSet(existingIPs, observedIPs) {
		applyRequest := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/apply", nil)
		applyRequest.RemoteAddr = "127.0.0.1:0"
		applyRequest.Header.Set(HeaderDNSSyncToken, server.dnsSyncToken)
		applyResponse := httptest.NewRecorder()
		server.handleRuntimeApply(applyResponse, applyRequest)
		runtimeApply = json.RawMessage(applyResponse.Body.Bytes())
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"rule_id":         req.RuleID,
		"set_name":        expectedSet,
		"members_applied": len(validMembers),
		"runtime_apply":   runtimeApply,
		"request_id":      requestID(r),
	})
}

func sameStringSet(left, right map[string]struct{}) bool {
	if len(left) != len(right) {
		return false
	}
	for value := range left {
		if _, ok := right[value]; !ok {
			return false
		}
	}
	return true
}

func (server *Server) validDNSSyncRequest(r *http.Request) bool {
	token := strings.TrimSpace(server.dnsSyncToken)
	if token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get(HeaderDNSSyncToken)), []byte(token)) != 1 {
		return false
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	address, err := netip.ParseAddr(host)
	return err == nil && address.IsLoopback()
}

func (server *Server) activeDNSPolicyResource(ctx context.Context) (DNSPolicyResource, error) {
	if server.store != nil {
		documents, err := server.store.Policies(ctx, "dns-policy")
		if err != nil {
			return DNSPolicyResource{}, err
		}
		merged := dns.NewPolicy(dns.Reject(), []dns.Rule{})
		merged.Engine = "smartdns"
		found := false
		lastPriority := 1000
		for _, document := range documents {
			if !document.Enabled {
				continue
			}
			var policy dns.Policy
			if err := json.Unmarshal(document.Payload, &policy); err != nil {
				return DNSPolicyResource{}, fmt.Errorf("decode DNS policy %q: %w", document.PolicyID, err)
			}
			for _, rule := range policy.Rules {
				if strings.TrimSpace(rule.ID) == "" {
					continue
				}
				// Policy IDs are user-editable. Prefixing only on collision keeps
				// DNS observation and diagnostics stable while preventing one
				// policy from shadowing another policy's rule ID.
				for _, existing := range merged.Rules {
					if existing.ID == rule.ID {
						rule.ID = document.PolicyID + ":" + rule.ID
						break
					}
				}
				merged.Rules = append(merged.Rules, rule)
			}
			// Policies are returned in ascending persistence priority. The
			// highest-priority-number policy supplies the final miss/default.
			merged.Miss = policy.Miss
			lastPriority = normalizedDNSPolicyPriority(document.Priority)
			found = true
		}
		if found {
			payload, marshalErr := json.Marshal(merged)
			if marshalErr != nil {
				return DNSPolicyResource{}, marshalErr
			}
			resource, resourceErr := server.dnsPolicyResource(ctx, "active", "Active DNS Policy", true, payload)
			if resourceErr != nil {
				return DNSPolicyResource{}, resourceErr
			}
			resource.Priority = lastPriority
			return resource, nil
		}
	}
	return server.defaultDNSPolicyResource(ctx)
}

func (server *Server) dnsPolicyResourceForID(ctx context.Context, id string) (DNSPolicyResource, error) {
	id = strings.TrimSpace(nonEmpty(id, "default"))
	if server.store != nil {
		document, err := server.store.Policy(ctx, "dns-policy", id)
		if err == nil {
			resource, resourceErr := server.dnsPolicyResource(ctx, document.PolicyID, document.PolicyID, document.Enabled, document.Payload)
			if resourceErr != nil {
				return DNSPolicyResource{}, resourceErr
			}
			resource.Priority = normalizedDNSPolicyPriority(document.Priority)
			return resource, nil
		}
		if !errors.Is(err, persistence.ErrNotFound) {
			return DNSPolicyResource{}, err
		}
	}
	resource, err := server.defaultDNSPolicyResource(ctx)
	if err != nil {
		return DNSPolicyResource{}, err
	}
	resource.ID = id
	resource.Name = nonEmpty(id, resource.Name)
	return resource, nil
}

func (server *Server) defaultDNSPolicyResource(ctx context.Context) (DNSPolicyResource, error) {
	payload, err := json.Marshal(dns.NewPolicy(dns.Reject(), []dns.Rule{}))
	if err != nil {
		return DNSPolicyResource{}, err
	}
	resource, err := server.dnsPolicyResource(ctx, "default", "Default DNS Policy", true, payload)
	if err != nil {
		return DNSPolicyResource{}, err
	}
	resource.Priority = normalizedDNSPolicyPriority(1000)
	return resource, nil
}

func normalizedDNSPolicyPriority(priority int) int {
	if priority <= 0 {
		return 1000
	}
	return priority
}

func (server *Server) handleDNSPolicyMutation(w http.ResponseWriter, r *http.Request, id string) {
	session, ok := server.sessionFromRequest(r)
	if !ok {
		server.recordAudit("anonymous", "system", r.URL.Path, "update", "denied", "authentication required", r)
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	if session.Role != "admin" {
		server.recordAudit(session.Username, session.Role, r.URL.Path, "update", "denied", "readonly mutation denied", r)
		writeError(w, r, http.StatusForbidden, "forbidden", "admin role required")
		return
	}
	if server.passwordChangeRequired(w, r, session, r.URL.Path, "update") {
		return
	}
	if server.store == nil {
		writeError(w, r, http.StatusServiceUnavailable, "store_unavailable", "local store is not configured")
		return
	}
	var req DNSPolicyResource
	if err := decodeStrictJSON(r, &req); err != nil {
		server.recordAudit(session.Username, session.Role, r.URL.Path, "update", "failure", err.Error(), r)
		writeError(w, r, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
		return
	}
	req.ID = id
	resource, payload, hash, err := server.compileDNSPolicyResource(r.Context(), req)
	if err != nil {
		server.recordAudit(session.Username, session.Role, r.URL.Path, "update", "failure", err.Error(), r)
		writeError(w, r, http.StatusUnprocessableEntity, "invalid_dns_policy", err.Error())
		return
	}
	if err := server.store.SavePolicy(r.Context(), persistence.PolicyDocument{Namespace: "dns-policy", PolicyID: resource.ID, Priority: resource.Priority, Enabled: resource.Enabled, Payload: payload, PayloadHash: hash, UpdatedAt: server.now().UTC()}); err != nil {
		server.recordAudit(session.Username, session.Role, r.URL.Path, "update", "failure", err.Error(), r)
		writeError(w, r, http.StatusInternalServerError, "dns_policy_save_failed", "DNS policy could not be saved")
		return
	}
	server.recordAudit(session.Username, session.Role, r.URL.Path, "update", "success", "", r)
	writeJSON(w, http.StatusOK, map[string]any{"item": resource, "request_id": requestID(r)})
}

func (server *Server) handleDNSPolicyDelete(w http.ResponseWriter, r *http.Request, id string) {
	session, ok := server.sessionFromRequest(r)
	if !ok {
		server.recordAudit("anonymous", "system", r.URL.Path, "delete", "denied", "authentication required", r)
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	if session.Role != "admin" {
		server.recordAudit(session.Username, session.Role, r.URL.Path, "delete", "denied", "readonly mutation denied", r)
		writeError(w, r, http.StatusForbidden, "forbidden", "admin role required")
		return
	}
	if server.passwordChangeRequired(w, r, session, r.URL.Path, "delete") {
		return
	}
	if server.store == nil {
		writeError(w, r, http.StatusServiceUnavailable, "store_unavailable", "local store is not configured")
		return
	}
	if err := server.store.DeletePolicy(r.Context(), "dns-policy", id); err != nil {
		if errors.Is(err, persistence.ErrNotFound) {
			server.recordAudit(session.Username, session.Role, r.URL.Path, "delete", "failure", "resource not found", r)
			writeError(w, r, http.StatusNotFound, "not_found", "resource was not found")
			return
		}
		server.recordAudit(session.Username, session.Role, r.URL.Path, "delete", "failure", err.Error(), r)
		writeError(w, r, http.StatusInternalServerError, "dns_policy_delete_failed", "DNS policy could not be deleted")
		return
	}
	server.recordAudit(session.Username, session.Role, r.URL.Path, "delete", "success", "", r)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id, "request_id": requestID(r)})
}

func (server *Server) handleMode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	response, err := server.modeResponse(r)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "mode_read_failed", "mode state is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (server *Server) handleModeInitialize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	session, ok := server.sessionFromRequest(r)
	if !ok {
		server.recordAudit("anonymous", "system", "/api/v1/mode/initialize", "initialize", "denied", "authentication required", r)
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	if session.Role != "admin" {
		server.recordAudit(session.Username, session.Role, "/api/v1/mode/initialize", "initialize", "denied", "admin role required", r)
		writeError(w, r, http.StatusForbidden, "forbidden", "admin role required")
		return
	}
	if server.passwordChangeRequired(w, r, session, "/api/v1/mode/initialize", "initialize") {
		return
	}
	var req ModeInitializeRequest
	if err := decodeStrictJSON(r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
		return
	}
	if strings.TrimSpace(req.Mode) != "gateway" {
		server.recordAudit(session.Username, session.Role, "/api/v1/mode/initialize", "initialize", "failure", "only gateway mode is supported", r)
		writeError(w, r, http.StatusUnprocessableEntity, "unsupported_mode", "only gateway mode is supported")
		return
	}
	if !req.ConfirmReset {
		server.recordAudit(session.Username, session.Role, "/api/v1/mode/initialize", "initialize", "failure", "configuration reset confirmation is required", r)
		writeError(w, r, http.StatusUnprocessableEntity, "confirmation_required", "configuration reset confirmation is required")
		return
	}
	response := ModeResponse{Mode: "gateway", Initialized: true, Switchable: false, PreserveAdminAccount: req.PreserveAdminAccount, PreserveManagementPort: req.PreserveManagementPort, Capabilities: []string{"gateway-only", "bridge-rejected", "local-runtime"}, RequestID: requestID(r)}
	if err := server.saveModeState(r.Context(), response); err != nil {
		server.recordAudit(session.Username, session.Role, "/api/v1/mode/initialize", "initialize", "failure", err.Error(), r)
		writeError(w, r, http.StatusInternalServerError, "mode_save_failed", "mode state could not be saved")
		return
	}
	server.recordAudit(session.Username, session.Role, "/api/v1/mode/initialize", "initialize", "success", "", r)
	writeJSON(w, http.StatusOK, response)
}

func (server *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": server.capabilities(r.Context()), "request_id": requestID(r)})
}

func (server *Server) modeResponse(r *http.Request) (ModeResponse, error) {
	response := ModeResponse{
		Mode:                   "gateway",
		Initialized:            false,
		Switchable:             false,
		PreserveAdminAccount:   false,
		PreserveManagementPort: false,
		Capabilities:           []string{"gateway-only", "bridge-rejected", "local-runtime"},
		RequestID:              requestID(r),
	}
	if server.store == nil {
		return response, nil
	}
	document, err := server.store.Config(r.Context(), "system_mode", "gateway")
	if errors.Is(err, persistence.ErrNotFound) {
		return response, nil
	}
	if err != nil {
		return ModeResponse{}, err
	}
	if err := json.Unmarshal(document.Payload, &response); err != nil {
		return ModeResponse{}, err
	}
	response.RequestID = requestID(r)
	return response, nil
}

func (server *Server) saveModeState(ctx context.Context, response ModeResponse) error {
	if server.store == nil {
		return nil
	}
	stored := response
	stored.RequestID = ""
	payload, hash, err := persistence.MarshalPayload(stored)
	if err != nil {
		return err
	}
	return server.store.SaveConfig(ctx, persistence.ConfigDocument{ResourceType: "system_mode", ResourceID: "gateway", Payload: payload, PayloadHash: hash, UpdatedAt: server.now().UTC()})
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

type authUserWriteRequest struct {
	Username string `json:"username"`
	Role     string `json:"role"`
	Password string `json:"password"`
	Enabled  *bool  `json:"enabled"`
}

func (server *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	if !server.auth.configured() {
		writeError(w, r, http.StatusServiceUnavailable, "auth_not_configured", "admin credentials are not configured")
		return
	}
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		server.recordAudit("anonymous", "system", "/api/v1/auth/login", "login", "failure", "invalid JSON", r)
		writeError(w, r, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
		return
	}
	actor := strings.TrimSpace(req.Username)
	if actor == "" {
		actor = "anonymous"
	}
	role, username, passwordChangeRequired := "", "", false
	if constantEqual(req.Username, server.auth.AdminUsername) && server.adminPasswordMatches(r.Context(), req.Password) {
		role = "admin"
		username = server.auth.AdminUsername
		passwordChangeRequired = server.adminPasswordChangeRequired(r.Context(), req.Password)
	} else if server.auth.readonlyConfigured() && constantEqual(req.Username, server.auth.ReadonlyUsername) && constantEqual(req.Password, server.auth.ReadonlyPassword) {
		role = "readonly"
		username = server.auth.ReadonlyUsername
	} else if server.store != nil {
		stored, err := server.store.AuthUser(r.Context(), req.Username)
		if err == nil && stored.PasswordHash != "" && constantEqual(stored.PasswordHash, hashPassword(stored.Username, req.Password)) {
			role = stored.Role
			username = stored.Username
			passwordChangeRequired = stored.PasswordChangeRequired
		}
	}
	if role == "" {
		server.recordAudit(actor, "system", "/api/v1/auth/login", "login", "failure", "invalid login", r)
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "invalid credentials")
		return
	}
	session := server.sessions.create(username, role, passwordChangeRequired)
	http.SetCookie(w, sessionCookie(session.ID, server.auth.CookieSecure))
	server.recordAudit(session.Username, session.Role, "/api/v1/auth/login", "login", "success", "", r)
	writeJSON(w, http.StatusOK, map[string]any{"session": publicSession(session), "password_change_required": passwordChangeRequired, "request_id": requestID(r)})
}

func (server *Server) adminPasswordMatches(ctx context.Context, password string) bool {
	if server.store != nil {
		stored, err := server.store.AuthUser(ctx, server.auth.AdminUsername)
		if err == nil && stored.PasswordHash != "" {
			return constantEqual(stored.PasswordHash, hashPassword(server.auth.AdminUsername, password))
		}
	}
	return constantEqual(password, server.auth.AdminPassword)
}

func (server *Server) adminPasswordChangeRequired(ctx context.Context, password string) bool {
	if !server.auth.ForcePasswordChange {
		return false
	}
	if server.store != nil {
		stored, err := server.store.AuthUser(ctx, server.auth.AdminUsername)
		if err == nil && stored.Stored {
			return stored.PasswordChangeRequired
		}
	}
	return constantEqual(password, server.auth.AdminPassword)
}

func (server *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	session, ok := server.sessionFromRequest(r)
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	if session.Role != "admin" || session.Username != server.auth.AdminUsername {
		server.recordAudit(session.Username, session.Role, "/api/v1/auth/change-password", "change-password", "denied", "admin role required", r)
		writeError(w, r, http.StatusForbidden, "forbidden", "admin role required")
		return
	}
	if server.store == nil {
		writeError(w, r, http.StatusServiceUnavailable, "store_unavailable", "local store is not configured")
		return
	}
	var req changePasswordRequest
	if err := decodeStrictJSON(r, &req); err != nil {
		server.recordAudit(session.Username, session.Role, "/api/v1/auth/change-password", "change-password", "failure", "invalid JSON", r)
		writeError(w, r, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
		return
	}
	if !server.adminPasswordMatches(r.Context(), req.CurrentPassword) {
		server.recordAudit(session.Username, session.Role, "/api/v1/auth/change-password", "change-password", "failure", "invalid current password", r)
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "current password is invalid")
		return
	}
	if constantEqual(req.CurrentPassword, req.NewPassword) {
		writeError(w, r, http.StatusUnprocessableEntity, "weak_password", "new password must be different from the current password")
		return
	}
	if err := validateNewPassword(session.Username, req.NewPassword); err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "weak_password", err.Error())
		return
	}
	if err := server.store.SaveAuthUser(r.Context(), persistence.AuthUser{Username: server.auth.AdminUsername, Role: "admin", PasswordHash: hashPassword(server.auth.AdminUsername, req.NewPassword), PasswordChangeRequired: false, UpdatedAt: server.now().UTC()}); err != nil {
		server.recordAudit(session.Username, session.Role, "/api/v1/auth/change-password", "change-password", "failure", err.Error(), r)
		writeError(w, r, http.StatusInternalServerError, "password_save_failed", "password could not be saved")
		return
	}
	server.sessions.delete(session.ID)
	newSession := server.sessions.create(server.auth.AdminUsername, "admin", false)
	http.SetCookie(w, sessionCookie(newSession.ID, server.auth.CookieSecure))
	server.recordAudit(session.Username, session.Role, "/api/v1/auth/change-password", "change-password", "success", "", r)
	writeJSON(w, http.StatusOK, map[string]any{"status": "password_changed", "session": publicSession(newSession), "password_change_required": false, "request_id": requestID(r)})
}

func (server *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	session, ok := server.sessionFromRequest(r)
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{"items": server.users(r.Context()), "request_id": requestID(r)})
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	if session.Role != "admin" {
		writeError(w, r, http.StatusForbidden, "forbidden", "admin role required")
		return
	}
	if server.passwordChangeRequired(w, r, session, "/api/v1/auth/users", "create-user") {
		return
	}
	if server.store == nil {
		writeError(w, r, http.StatusServiceUnavailable, "store_unavailable", "local store is not configured")
		return
	}
	var req authUserWriteRequest
	if err := decodeStrictJSON(r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
		return
	}
	user, err := authUserFromRequest(req, server.now().UTC())
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "invalid_user", err.Error())
		return
	}
	if err := server.store.SaveAuthUser(r.Context(), user); err != nil {
		writeError(w, r, http.StatusInternalServerError, "user_save_failed", "user could not be saved")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": publicAuthUser(user), "request_id": requestID(r)})
}

func (server *Server) handleUserItem(w http.ResponseWriter, r *http.Request) {
	session, ok := server.sessionFromRequest(r)
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	if session.Role != "admin" {
		writeError(w, r, http.StatusForbidden, "forbidden", "admin role required")
		return
	}
	if server.passwordChangeRequired(w, r, session, "/api/v1/auth/users/", "mutate-user") {
		return
	}
	if server.store == nil {
		writeError(w, r, http.StatusServiceUnavailable, "store_unavailable", "local store is not configured")
		return
	}
	username, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/api/v1/auth/users/"))
	if err != nil || strings.TrimSpace(username) == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_user", "username is required")
		return
	}
	switch r.Method {
	case http.MethodPatch:
		var req authUserWriteRequest
		if err := decodeStrictJSON(r, &req); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
			return
		}
		if strings.TrimSpace(req.Username) == "" {
			req.Username = username
		}
		if req.Username != username {
			writeError(w, r, http.StatusUnprocessableEntity, "username_mismatch", "username path and body must match")
			return
		}
		existing, err := server.store.AuthUser(r.Context(), username)
		if err != nil {
			writeError(w, r, http.StatusNotFound, "not_found", "user was not found")
			return
		}
		user, err := updatedAuthUser(existing, req, server.now().UTC())
		if err != nil {
			writeError(w, r, http.StatusUnprocessableEntity, "invalid_user", err.Error())
			return
		}
		if err := server.store.SaveAuthUser(r.Context(), user); err != nil {
			writeError(w, r, http.StatusInternalServerError, "user_save_failed", "user could not be saved")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"item": publicAuthUser(user), "request_id": requestID(r)})
	case http.MethodDelete:
		if username == server.auth.AdminUsername || username == session.Username {
			writeError(w, r, http.StatusUnprocessableEntity, "protected_user", "cannot delete the built-in admin or current user")
			return
		}
		if err := server.store.DeleteAuthUser(r.Context(), username); err != nil {
			writeError(w, r, http.StatusNotFound, "not_found", "user was not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "deleted", "request_id": requestID(r)})
	default:
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
	}
}

func (server *Server) users(ctx context.Context) []User {
	users := []User{{Username: server.auth.AdminUsername, Role: "admin", Enabled: server.auth.configured()}}
	if strings.TrimSpace(server.auth.ReadonlyUsername) != "" {
		users = append(users, User{Username: server.auth.ReadonlyUsername, Role: "readonly", Enabled: server.auth.readonlyConfigured()})
	}
	if server.store != nil {
		stored, err := server.store.AuthUsers(ctx)
		if err == nil {
			for _, user := range stored {
				if user.Username == server.auth.AdminUsername || user.Username == server.auth.ReadonlyUsername {
					continue
				}
				users = append(users, publicAuthUser(user))
			}
		}
	}
	return users
}

func authUserFromRequest(req authUserWriteRequest, updatedAt time.Time) (persistence.AuthUser, error) {
	username := strings.TrimSpace(req.Username)
	role := strings.TrimSpace(req.Role)
	if username == "" {
		return persistence.AuthUser{}, fmt.Errorf("username is required")
	}
	if role == "" {
		role = "readonly"
	}
	if role != "admin" && role != "readonly" {
		return persistence.AuthUser{}, fmt.Errorf("role must be admin or readonly")
	}
	if err := validateNewPassword(username, req.Password); err != nil {
		return persistence.AuthUser{}, err
	}
	return persistence.AuthUser{Username: username, Role: role, PasswordHash: hashPassword(username, req.Password), PasswordChangeRequired: false, UpdatedAt: updatedAt}, nil
}

func updatedAuthUser(existing persistence.AuthUser, req authUserWriteRequest, updatedAt time.Time) (persistence.AuthUser, error) {
	user := existing
	if strings.TrimSpace(req.Role) != "" {
		if req.Role != "admin" && req.Role != "readonly" {
			return persistence.AuthUser{}, fmt.Errorf("role must be admin or readonly")
		}
		user.Role = req.Role
	}
	if strings.TrimSpace(req.Password) != "" {
		if err := validateNewPassword(user.Username, req.Password); err != nil {
			return persistence.AuthUser{}, err
		}
		user.PasswordHash = hashPassword(user.Username, req.Password)
		user.PasswordChangeRequired = false
	}
	user.UpdatedAt = updatedAt
	return user, nil
}

func publicAuthUser(user persistence.AuthUser) User {
	return User{Username: user.Username, Role: user.Role, Enabled: user.PasswordHash != ""}
}

func (server *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	session, ok := server.sessionFromRequest(r)
	if !ok {
		server.recordAudit("anonymous", "system", "/api/v1/auth/logout", "logout", "failure", "missing session", r)
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	server.sessions.delete(session.ID)
	http.SetCookie(w, clearSessionCookie(server.auth.CookieSecure))
	server.recordAudit(session.Username, session.Role, "/api/v1/auth/logout", "logout", "success", "", r)
	writeJSON(w, http.StatusOK, map[string]any{"status": "logged_out", "request_id": requestID(r)})
}

func (server *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	session, ok := server.sessionFromRequest(r)
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": publicSession(session), "password_change_required": session.PasswordChangeRequired, "request_id": requestID(r)})
}

func publicSession(session Session) map[string]any {
	return map[string]any{
		"username":                 session.Username,
		"role":                     session.Role,
		"created_at":               session.CreatedAt,
		"password_change_required": session.PasswordChangeRequired,
	}
}

func (server *Server) passwordChangeRequired(w http.ResponseWriter, r *http.Request, session Session, resource, action string) bool {
	if !session.PasswordChangeRequired {
		return false
	}
	server.recordAudit(session.Username, session.Role, resource, action, "denied", "password change required", r)
	writeError(w, r, http.StatusForbidden, "password_change_required", "password change is required before continuing")
	return true
}

func (server *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	dependencies := server.capabilities(r.Context())
	status := "ok"
	degraded := false
	for _, dependency := range dependencies {
		if requiredCapability(dependency.Name) && !dependency.Available {
			status = "degraded"
			degraded = true
			break
		}
	}
	writeJSON(w, http.StatusOK, HealthResponse{Status: status, Version: server.version, Degraded: degraded, RequestID: requestID(r), Dependencies: dependencies})
}

func requiredCapability(name string) bool {
	switch name {
	case "vpp", "smartdns", "kea", "xray", "nftables", "linux_routing", "persistence":
		return true
	default:
		return false
	}
}

func (server *Server) handleDashboardSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	if _, ok := server.sessionFromRequest(r); !ok {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	dependencies := server.capabilities(r.Context())
	system, systemCapability := readSystemSummary()
	status := "ok"
	degraded := !systemCapability.Available
	for _, dependency := range dependencies {
		if requiredCapability(dependency.Name) && !dependency.Available {
			status = "degraded"
			degraded = true
			break
		}
	}
	if degraded {
		status = "degraded"
	}
	capabilities := append([]controlapi.CapabilityState{}, dependencies...)
	capabilities = append(capabilities, systemCapability)
	writeJSON(w, http.StatusOK, map[string]any{"status": status, "degraded": degraded, "dependencies": dependencies, "system": system, "capabilities": capabilities, "request_id": requestID(r)})
}

func (server *Server) handleTrafficTrend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	if _, ok := server.sessionFromRequest(r); !ok {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	window := strings.TrimSpace(r.URL.Query().Get("window"))
	if window == "" {
		window = "24h"
	}
	points := clampIntQuery(r.URL.Query().Get("points"), 288, 1, 288)
	if server.profile.ID() == product.Orchestrator().ID() {
		result, capability := server.orchestratorTrafficTrend(r.Context(), TrafficTrendQuery{Window: window, Points: points})
		writeTrafficTrendResult(w, r, result, capability)
		return
	}
	if server.trafficTrend != nil {
		result, err := server.trafficTrend.TrafficTrend(r.Context(), TrafficTrendQuery{Window: window, Points: points})
		if err == nil {
			writeTrafficTrendResult(w, r, normalizeTrafficTrendResult(result, window, points), controlapi.CapabilityState{Name: "traffic_trend_sampler", Available: true, State: controlapi.CapabilityAvailable})
			return
		}
		capability := controlapi.CapabilityState{Name: "traffic_trend_sampler", Available: false, State: controlapi.CapabilityDegraded, Reason: err.Error()}
		writeTrafficTrendResult(w, r, unavailableTrafficTrend(window, points, capability.Reason), capability)
		return
	}
	if server.gatewayTelemetry != nil {
		err := server.collectGatewayTelemetry(r.Context())
		result := server.gatewayTrafficTrend(TrafficTrendQuery{Window: window, Points: points})
		capability := controlapi.CapabilityState{Name: "gateway_logical_egress", Available: err == nil, State: controlapi.CapabilityAvailable}
		if result.Degraded {
			capability.Available = false
			capability.State = controlapi.CapabilityDegraded
			capability.Reason = result.DegradedReason
		}
		writeTrafficTrendResult(w, r, result, capability)
		return
	}
	capability := controlapi.CapabilityState{Name: "gateway_logical_egress", Available: false, State: controlapi.CapabilityDegraded, Reason: "gateway telemetry collector is not configured"}
	writeTrafficTrendResult(w, r, unavailableTrafficTrend(window, points, capability.Reason), capability)
}

func writeTrafficTrendResult(w http.ResponseWriter, r *http.Request, result TrafficTrendResult, capability controlapi.CapabilityState) {
	writeJSON(w, http.StatusOK, map[string]any{"window": result.Window, "points": result.Points, "sampling_interval_seconds": result.SamplingIntervalSeconds, "state": result.State, "totals": result.Totals, "series": result.Series, "degraded": result.Degraded, "degraded_reason": result.DegradedReason, "capabilities": []controlapi.CapabilityState{capability}, "request_id": requestID(r)})
}

func normalizeTrafficTrendResult(result TrafficTrendResult, window string, points int) TrafficTrendResult {
	if result.Window == "" {
		result.Window = window
	}
	if result.Points == 0 {
		result.Points = points
	}
	if result.SamplingIntervalSeconds == 0 {
		result.SamplingIntervalSeconds = 300
	}
	if result.State == "" {
		result.State = "available"
	}
	if result.Series.LogicalEgresses == nil {
		result.Series.LogicalEgresses = []LogicalEgressSeries{}
	}
	return result
}

func unavailableTrafficTrend(window string, points int, reason string) TrafficTrendResult {
	return TrafficTrendResult{Window: window, Points: points, SamplingIntervalSeconds: 300, State: "unavailable", Degraded: true, DegradedReason: reason, Series: TrafficTrendSets{LogicalEgresses: []LogicalEgressSeries{}}}
}

func numberField(item map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		switch value := item[key].(type) {
		case int:
			return float64(value), true
		case int64:
			return float64(value), true
		case float64:
			return value, true
		case json.Number:
			parsed, err := value.Float64()
			return parsed, err == nil
		}
	}
	return 0, false
}

func (server *Server) capabilities(ctx context.Context) []controlapi.CapabilityState {
	states := []controlapi.CapabilityState{}
	if server.profile.AllowsService(product.ServiceVPP) {
		states = append(states, server.serviceCapability(ctx, serviceRuntime.VPP, "vpp", "VPP apply runtime is not configured"))
	}
	if server.profile.AllowsService(product.ServiceSmartDNS) {
		states = append(states, server.serviceCapability(ctx, serviceRuntime.SmartDNS, "smartdns", "SmartDNS service runtime is not configured"))
	}
	if server.profile.AllowsService(product.ServiceKea) {
		states = append(states, server.serviceCapability(ctx, serviceRuntime.Kea, "kea", "Kea service runtime is not configured"))
	}
	if server.profile.AllowsService(product.ServiceXray) {
		states = append(states, server.serviceCapability(ctx, serviceRuntime.Xray, "xray", "xray service runtime is not configured"))
	}
	if server.profile.AllowsService(product.ServicePPPoE) {
		states = append(states, server.optionalCapability(ctx, serviceRuntime.PPPoE, "pppoe", "PPPoE service runtime is not configured"))
	}
	if server.profile.AllowsService(product.ServiceNftables) {
		states = append(states, server.serviceCapability(ctx, serviceRuntime.Nftables, "nftables", "nftables service runtime is not configured"))
	}
	if server.profile.AllowsService(product.ServiceLinuxRouting) {
		states = append(states, server.serviceCapability(ctx, serviceRuntime.LinuxRouting, "linux_routing", "Linux policy routing service runtime is not configured"))
	}
	if server.store == nil {
		states = append(states, controlapi.CapabilityState{Name: "persistence", Available: false, State: controlapi.CapabilityDegraded, Reason: "SQLite store not configured"})
	} else {
		states = append(states, controlapi.CapabilityState{Name: "persistence", Available: true, State: controlapi.CapabilityAvailable})
	}
	return states
}

func (server *Server) optionalCapability(ctx context.Context, service serviceRuntime.ServiceName, name, unavailableReason string) controlapi.CapabilityState {
	if !server.serviceRuntimeConfigured() {
		return controlapi.CapabilityState{Name: name, Available: false, State: controlapi.CapabilityAvailable, Reason: unavailableReason}
	}
	health, err := server.services.HealthCheck(ctx, service)
	if err != nil {
		return controlapi.CapabilityState{Name: name, Available: false, State: controlapi.CapabilityAvailable, Reason: err.Error()}
	}
	if len(health) == 0 || !health[0].Available {
		reason := serviceUnitReason(service)
		if len(health) > 0 && strings.TrimSpace(health[0].Reason) != "" {
			reason = health[0].Reason
		}
		return controlapi.CapabilityState{Name: name, Available: false, State: controlapi.CapabilityAvailable, Reason: reason}
	}
	return controlapi.CapabilityState{Name: name, Available: true, State: controlapi.CapabilityAvailable}
}

func (server *Server) serviceCapability(ctx context.Context, service serviceRuntime.ServiceName, name, unavailableReason string) controlapi.CapabilityState {
	if !server.serviceRuntimeConfigured() {
		return controlapi.CapabilityState{Name: name, Available: false, State: controlapi.CapabilityDegraded, Reason: unavailableReason}
	}
	health, err := server.services.HealthCheck(ctx, service)
	if err != nil {
		return controlapi.CapabilityState{Name: name, Available: false, State: controlapi.CapabilityDegraded, Reason: err.Error()}
	}
	if len(health) == 0 || !health[0].Available {
		reason := serviceUnitReason(service)
		if len(health) > 0 && strings.TrimSpace(health[0].Reason) != "" {
			reason = health[0].Reason
		}
		return controlapi.CapabilityState{Name: name, Available: false, State: controlapi.CapabilityDegraded, Reason: reason}
	}
	return controlapi.CapabilityState{Name: name, Available: true, State: controlapi.CapabilityAvailable}
}

func (server *Server) runtimeCapability(service serviceRuntime.ServiceName, name, unavailableReason string) controlapi.RuntimeCapability {
	state := server.serviceCapability(context.Background(), service, name, unavailableReason)
	return controlapi.RuntimeCapability{Name: name, Available: state.Available, Reason: state.Reason}
}

func serviceUnitReason(service serviceRuntime.ServiceName) string {
	return string(service) + " service is not active"
}

func (server *Server) handleProxyEgresses(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		items, err := server.proxyEgressResources(r.Context())
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "proxy_egress_read_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "request_id": requestID(r)})
	case http.MethodPost:
		session, ok := server.sessionFromRequest(r)
		if !ok {
			server.recordAudit("anonymous", "system", "/api/v1/proxy/egresses", "create", "denied", "authentication required", r)
			writeError(w, r, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		if session.Role != "admin" {
			server.recordAudit(session.Username, session.Role, "/api/v1/proxy/egresses", "create", "denied", "readonly mutation denied", r)
			writeError(w, r, http.StatusForbidden, "forbidden", "admin role required")
			return
		}
		if server.passwordChangeRequired(w, r, session, "/api/v1/proxy/egresses", "create") {
			return
		}
		if server.store == nil {
			writeError(w, r, http.StatusServiceUnavailable, "store_unavailable", "local store is not configured")
			return
		}
		var req ProxyEgressWriteRequest
		if err := decodeStrictJSON(r, &req); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
			return
		}
		resource, payload, hash, err := server.compileProxyEgressResource(req)
		if err != nil {
			server.recordAudit(session.Username, session.Role, "/api/v1/proxy/egresses", "create", "failure", err.Error(), r)
			writeError(w, r, http.StatusUnprocessableEntity, "invalid_proxy_egress", err.Error())
			return
		}
		if err := server.store.SaveConfig(r.Context(), persistence.ConfigDocument{ResourceType: "proxy_egress", ResourceID: resource.ID, Payload: payload, PayloadHash: hash, UpdatedAt: server.now().UTC()}); err != nil {
			server.recordAudit(session.Username, session.Role, "/api/v1/proxy/egresses", "create", "failure", err.Error(), r)
			writeError(w, r, http.StatusInternalServerError, "proxy_egress_save_failed", "proxy egress could not be saved")
			return
		}
		server.recordAudit(session.Username, session.Role, "/api/v1/proxy/egresses", "create", "success", "", r)
		writeJSON(w, http.StatusOK, map[string]any{"item": resource, "request_id": requestID(r)})
	default:
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
	}
}

func (server *Server) proxyEgressResources(ctx context.Context) ([]controlapi.ProxyEgressResource, error) {
	if server.store != nil {
		documents, err := server.store.Configs(ctx, "proxy_egress")
		if err != nil {
			return nil, err
		}
		if len(documents) > 0 {
			items := make([]controlapi.ProxyEgressResource, 0, len(documents))
			for _, document := range documents {
				var payload map[string]any
				if err := json.Unmarshal(document.Payload, &payload); err != nil {
					return nil, err
				}
				if truthy(payload["deleted"]) {
					continue
				}
				var egress proxy.Egress
				if err := json.Unmarshal(document.Payload, &egress); err != nil {
					return nil, err
				}
				name := nonEmpty(stringField(payload, "name"), document.ResourceID)
				enabled := true
				if configured, ok := payload["enabled"].(bool); ok {
					enabled = configured
				}
				resource, err := server.proxyEgressResource(egress, name, enabled)
				if err != nil {
					return nil, err
				}
				resource.UnderlayWANID = stringField(payload, "underlay_wan_id")
				items = append(items, resource)
			}
			return items, nil
		}
	}
	resource, err := server.proxyEgressResource(server.proxyEgress, "Default proxy egress", true)
	if err != nil {
		return nil, err
	}
	return []controlapi.ProxyEgressResource{resource}, nil
}

func (server *Server) proxyEgressResource(egress proxy.Egress, name string, enabled bool) (controlapi.ProxyEgressResource, error) {
	return controlapi.ProxyEgressWANRow(egress, name, enabled,
		server.runtimeCapability(serviceRuntime.VPP, "vpp_steering", "VPP apply runtime is not configured"),
		server.runtimeCapability(serviceRuntime.Xray, "xray_runtime", "xray service runtime is not configured"),
	)

}

func (server *Server) handleProxyXrayRuntime(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if action == "restart" && r.Method != http.MethodPost || action != "restart" && r.Method != http.MethodGet {
			writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
			return
		}
		session, ok := server.sessionFromRequest(r)
		if !ok {
			server.recordAudit("anonymous", "system", r.URL.Path, action, "denied", "authentication required", r)
			writeError(w, r, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		if action == "restart" && session.Role != "admin" {
			server.recordAudit(session.Username, session.Role, r.URL.Path, action, "denied", "admin role required", r)
			writeError(w, r, http.StatusForbidden, "forbidden", "admin role required")
			return
		}
		if action == "restart" && server.passwordChangeRequired(w, r, session, r.URL.Path, action) {
			return
		}
		if !server.serviceRuntimeConfigured() {
			writeJSON(w, http.StatusOK, map[string]any{"service": "xray", "state": "degraded", "available": false, "reason": "xray service runtime is not configured", "request_id": requestID(r)})
			return
		}
		switch action {
		case "status":
			health, err := server.services.HealthCheck(r.Context(), serviceRuntime.Xray)
			if err != nil {
				writeError(w, r, http.StatusInternalServerError, "xray_status_failed", err.Error())
				return
			}
			item := serviceRuntime.Health{Service: serviceRuntime.Xray, Available: false, Reason: "xray status unavailable"}
			if len(health) > 0 {
				item = health[0]
			}
			balancers := []map[string]any{}
			if item.Available {
				balancers, err = server.xrayRuntimeSelections(r.Context())
				if err != nil {
					item.Available = false
					item.Reason = serviceRuntime.Redact(err.Error())
				}
			}
			writeJSON(w, http.StatusOK, map[string]any{"service": "xray", "state": availabilityState(item.Available), "available": item.Available, "reason": item.Reason, "balancers": balancers, "request_id": requestID(r)})
		case "logs":
			controller, ok := server.services.Controller.(serviceRuntime.LogController)
			if !ok {
				writeJSON(w, http.StatusOK, map[string]any{"service": "xray", "state": "degraded", "available": false, "reason": "xray log reader is not configured", "logs": "", "request_id": requestID(r)})
				return
			}
			logs, err := controller.Logs(r.Context(), serviceRuntime.Xray, 100)
			if err != nil {
				writeJSON(w, http.StatusOK, map[string]any{"service": "xray", "state": "degraded", "available": false, "reason": err.Error(), "logs": serviceRuntime.Redact(logs), "request_id": requestID(r)})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"service": "xray", "state": "available", "available": true, "logs": serviceRuntime.Redact(logs), "request_id": requestID(r)})
		case "restart":
			egress, configured, err := server.runtimeProxyEgress(r.Context())
			if err != nil {
				server.recordAudit(session.Username, session.Role, r.URL.Path, action, "failure", err.Error(), r)
				writeError(w, r, http.StatusUnprocessableEntity, "xray_restart_plan_failed", err.Error())
				return
			}
			if !configured {
				writeError(w, r, http.StatusUnprocessableEntity, "xray_artifacts_missing", "proxy egress is not configured")
				return
			}
			compiled, err := proxy.CompileEgress(egress)
			if err == nil {
				if warning := server.compileProxySubscription(r.Context(), egress, &compiled); warning != "" {
					err = errors.New(warning)
				}
			}
			if err != nil {
				server.recordAudit(session.Username, session.Role, r.URL.Path, action, "failure", err.Error(), r)
				writeError(w, r, http.StatusUnprocessableEntity, "xray_restart_plan_failed", err.Error())
				return
			}
			proxy.ApplyOutboundSocketMark(&compiled.XrayRuntime.ConfigPayload, compiled.ServiceNetwork.OutboundMark)
			artifacts, err := serviceRuntime.RenderXray(compiled)
			if err != nil || len(artifacts) == 0 {
				server.recordAudit(session.Username, session.Role, r.URL.Path, action, "failure", "xray artifacts are not rendered", r)
				writeError(w, r, http.StatusUnprocessableEntity, "xray_artifacts_missing", "xray artifacts are not rendered")
				return
			}
			if err := server.services.Controller.ReloadOrRestart(r.Context(), serviceRuntime.Xray, artifacts); err != nil {
				reason := serviceRuntime.Redact(err.Error())
				server.recordAudit(session.Username, session.Role, r.URL.Path, action, "failure", reason, r)
				writeError(w, r, http.StatusServiceUnavailable, "xray_restart_failed", reason)
				return
			}
			balancers, err := server.xrayRuntimeSelections(r.Context())
			if err != nil {
				reason := serviceRuntime.Redact(err.Error())
				server.recordAudit(session.Username, session.Role, r.URL.Path, action, "failure", reason, r)
				writeError(w, r, http.StatusServiceUnavailable, "xray_readback_failed", reason)
				return
			}
			server.recordAudit(session.Username, session.Role, r.URL.Path, action, "success", "", r)
			writeJSON(w, http.StatusOK, map[string]any{"service": "xray", "status": "restarted", "runtime_state": "running", "balancers": balancers, "artifacts": summarizeRuntimeArtifacts(artifacts), "request_id": requestID(r)})
		}
	}
}

func (server *Server) xrayRuntimeSelections(ctx context.Context) ([]map[string]any, error) {
	items, err := server.desiredItems(ctx, "proxy_subscription")
	if err != nil {
		return nil, fmt.Errorf("read proxy subscriptions for Xray state: %w", err)
	}
	tags := make([]string, 0)
	subscriptionByTag := map[string]string{}
	for _, item := range items {
		if enabled, exists := item["enabled"].(bool); exists && !enabled {
			continue
		}
		if strings.TrimSpace(stringField(item, "selection")) != string(proxy.SelectionFastest) {
			continue
		}
		id := strings.TrimSpace(stringField(item, "id"))
		if id == "" {
			return nil, fmt.Errorf("enabled fastest proxy subscription has no ID")
		}
		tag := "subscription-" + id + "-fastest"
		tags = append(tags, tag)
		subscriptionByTag[tag] = id
	}
	if len(tags) == 0 {
		return []map[string]any{}, nil
	}
	controller, ok := server.services.Controller.(serviceRuntime.XrayRoutingStateController)
	if !ok {
		return nil, fmt.Errorf("Xray routing state reader is not configured")
	}
	states, err := controller.XrayBalancerStates(ctx, tags)
	if err != nil {
		return nil, err
	}
	result := make([]map[string]any, 0, len(states))
	for _, state := range states {
		subscriptionID := subscriptionByTag[state.Tag]
		prefix := "subscription-" + subscriptionID + "-node-"
		nodeIDs := make([]string, 0, len(state.SelectedOutboundTags))
		for _, outboundTag := range state.SelectedOutboundTags {
			if !strings.HasPrefix(outboundTag, prefix) || strings.TrimPrefix(outboundTag, prefix) == "" {
				return nil, fmt.Errorf("Xray balancer %s selected unexpected outbound %q", state.Tag, outboundTag)
			}
			nodeIDs = append(nodeIDs, strings.TrimPrefix(outboundTag, prefix))
		}
		result = append(result, map[string]any{"subscription_id": subscriptionID, "balancer_tag": state.Tag, "selection": "fastest", "selected_node_ids": nodeIDs, "selected_outbound_tags": state.SelectedOutboundTags, "live_verified": true})
	}
	return result, nil
}

func availabilityState(available bool) string {
	if available {
		return "available"
	}
	return "degraded"
}

func artifactsForService(artifacts []serviceRuntime.RenderedArtifact, service serviceRuntime.ServiceName) []serviceRuntime.RenderedArtifact {
	items := make([]serviceRuntime.RenderedArtifact, 0)
	for _, artifact := range artifacts {
		if artifact.Service == service {
			items = append(items, artifact)
		}
	}
	return items
}

func (server *Server) compileProxyEgressResource(req ProxyEgressWriteRequest) (controlapi.ProxyEgressResource, json.RawMessage, string, error) {
	if strings.TrimSpace(req.ID) == "" {
		return controlapi.ProxyEgressResource{}, nil, "", fmt.Errorf("proxy egress id is required")
	}
	if req.Kind != "" && req.Kind != controlapi.ResourceKindProxyEgress {
		return controlapi.ProxyEgressResource{}, nil, "", fmt.Errorf("proxy egress kind must be %q", controlapi.ResourceKindProxyEgress)
	}
	if req.SemanticType != "" && req.SemanticType != proxy.ProxyEgress {
		return controlapi.ProxyEgressResource{}, nil, "", fmt.Errorf("semantic_type must be %q", proxy.ProxyEgress)
	}
	if req.DisplayList != "" && req.DisplayList != proxy.WANDisplayList {
		return controlapi.ProxyEgressResource{}, nil, "", fmt.Errorf("display_list must be %q", proxy.WANDisplayList)
	}
	if strings.TrimSpace(req.UnderlayWANID) == "" {
		return controlapi.ProxyEgressResource{}, nil, "", fmt.Errorf("proxy egress requires underlay_wan_id")
	}
	if req.LowCopy {
		return controlapi.ProxyEgressResource{}, nil, "", fmt.Errorf("poc_failed: low-copy proxy handoff requires Task 16 PASS")
	}
	profile := proxy.RuntimeProfile(nonEmpty(req.ProxyProfileID, "xray-tproxy-outbound"))
	egress := proxy.NewProxyEgress(req.ID, profile)
	egress.UnderlayWANID = strings.TrimSpace(req.UnderlayWANID)
	egress.NodeID = strings.TrimSpace(req.NodeID)
	egress.SubscriptionID = strings.TrimSpace(req.SubscriptionID)
	if err := proxy.ValidateEgress(egress); err != nil {
		return controlapi.ProxyEgressResource{}, nil, "", err
	}
	payloadObject := map[string]any{"id": egress.ID, "name": nonEmpty(req.Name, req.ID), "enabled": req.Enabled, "semantic_type": egress.SemanticType, "display_list": egress.DisplayList, "runtime_profile": egress.RuntimeProfile, "capture_path": egress.CapturePath, "engine": egress.Engine, "handoff": egress.Handoff, "listener_mode": egress.ListenerMode, "underlay_wan_id": egress.UnderlayWANID}
	if egress.NodeID != "" {
		payloadObject["node_id"] = egress.NodeID
	}
	if egress.SubscriptionID != "" {
		payloadObject["subscription_id"] = egress.SubscriptionID
	}
	if strings.TrimSpace(req.Description) != "" {
		payloadObject["description"] = strings.TrimSpace(req.Description)
	}
	payload, hash, err := persistence.MarshalPayload(payloadObject)
	if err != nil {
		return controlapi.ProxyEgressResource{}, nil, "", err
	}
	resource, err := server.proxyEgressResource(egress, nonEmpty(req.Name, req.ID), req.Enabled)
	resource.UnderlayWANID = egress.UnderlayWANID
	resource.NodeID = egress.NodeID
	resource.SubscriptionID = egress.SubscriptionID
	return resource, payload, hash, err
}

func (server *Server) handleDefaultFlowIntent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	resource := controlapi.FlowIntent(server.flowIntent,
		server.runtimeCapability(serviceRuntime.VPP, "vpp_qos_push", "VPP apply runtime is not configured"),
	)
	writeJSON(w, http.StatusOK, map[string]any{"item": resource, "request_id": requestID(r)})
}

func (server *Server) handleInterfaceBonds(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		items, err := server.desiredItems(r.Context(), "interface_bond")
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "interface_bond_read_failed", "interface bonds are unavailable")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "capabilities": server.degradedRuntimeCapabilities(), "request_id": requestID(r)})
	case http.MethodPost:
		server.handleInterfaceBondCreate(w, r)
	default:
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
	}
}

func (server *Server) handleInterfaceBondCreate(w http.ResponseWriter, r *http.Request) {
	session, ok := server.sessionFromRequest(r)
	if !ok {
		server.recordAudit("anonymous", "system", r.URL.Path, "create", "denied", "authentication required", r)
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	if session.Role != "admin" {
		server.recordAudit(session.Username, session.Role, r.URL.Path, "create", "denied", "readonly mutation denied", r)
		writeError(w, r, http.StatusForbidden, "forbidden", "admin role required")
		return
	}
	if server.passwordChangeRequired(w, r, session, r.URL.Path, "create") {
		return
	}
	if server.store == nil {
		writeError(w, r, http.StatusServiceUnavailable, "store_unavailable", "local store is not configured")
		return
	}
	var payload map[string]any
	if err := decodeStrictJSON(r, &payload); err != nil {
		server.recordAudit(session.Username, session.Role, r.URL.Path, "create", "failure", "request body must be valid JSON", r)
		writeError(w, r, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
		return
	}
	item, status, err := server.compileInterfaceBond(r.Context(), payload)
	if err != nil {
		server.recordAudit(session.Username, session.Role, r.URL.Path, "create", "failure", err.Error(), r)
		writeError(w, r, status, "invalid_interface_bond", err.Error())
		return
	}
	raw, hash, err := persistence.MarshalPayload(item)
	if err != nil {
		server.recordAudit(session.Username, session.Role, r.URL.Path, "create", "failure", err.Error(), r)
		writeError(w, r, http.StatusBadRequest, "invalid_payload", "interface bond payload could not be encoded")
		return
	}
	if err := server.store.SaveConfig(r.Context(), persistence.ConfigDocument{ResourceType: "interface_bond", ResourceID: stringField(item, "id"), Payload: raw, PayloadHash: hash, UpdatedAt: server.now().UTC()}); err != nil {
		server.recordAudit(session.Username, session.Role, r.URL.Path, "create", "failure", err.Error(), r)
		writeError(w, r, http.StatusInternalServerError, "interface_bond_save_failed", "interface bond could not be saved")
		return
	}
	server.recordAudit(session.Username, session.Role, r.URL.Path, "create", "success", "", r)
	writeJSON(w, http.StatusOK, map[string]any{"item": item, "request_id": requestID(r), "runtime_state": "desired_not_applied"})
}

func (server *Server) compileInterfaceBond(ctx context.Context, payload map[string]any) (map[string]any, int, error) {
	id := strings.TrimSpace(nonEmpty(stringField(payload, "id"), stringField(payload, "name")))
	if id == "" {
		id = "bond-" + strings.ReplaceAll(newRequestID(), "-", "")[:8]
	}
	members := stringSliceField(payload, "members")
	if len(members) < 2 {
		return nil, http.StatusBadRequest, fmt.Errorf("interface bond requires at least two members")
	}
	interfaces, _, err := server.interfaceRuntimeSnapshot(ctx)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	byID := map[string]map[string]any{}
	for _, item := range interfaces {
		for _, key := range []string{stringField(item, "id"), stringField(item, "name")} {
			if key != "" {
				byID[key] = item
			}
		}
	}
	used := map[string]bool{}
	selected := make([]map[string]any, 0, len(members))
	for _, member := range members {
		member = strings.TrimSpace(member)
		if member == "" || used[member] {
			return nil, http.StatusBadRequest, fmt.Errorf("interface bond members must be non-empty and unique")
		}
		used[member] = true
		item, ok := byID[member]
		if !ok {
			return nil, http.StatusBadRequest, fmt.Errorf("interface bond member %q does not exist", member)
		}
		if stringField(item, "bond") != "" || stringField(item, "lag") != "" || stringField(item, "bond_id") != "" || stringField(item, "member_of") != "" {
			return nil, http.StatusConflict, fmt.Errorf("interface bond member %q is already bound", member)
		}
		selected = append(selected, item)
	}
	speed := stringField(selected[0], "speed")
	workMode := stringField(selected[0], "work_mode")
	if workMode == "" {
		workMode = stringField(selected[0], "active_path")
	}
	for _, item := range selected[1:] {
		if stringField(item, "speed") != speed {
			return nil, http.StatusBadRequest, fmt.Errorf("interface bond members must have the same speed")
		}
		itemWorkMode := stringField(item, "work_mode")
		if itemWorkMode == "" {
			itemWorkMode = stringField(item, "active_path")
		}
		if itemWorkMode != workMode {
			return nil, http.StatusBadRequest, fmt.Errorf("interface bond members must have the same work_mode")
		}
	}
	return map[string]any{"id": id, "name": nonEmpty(stringField(payload, "name"), id), "members": members, "mode": "xor", "load_balance": "l34", "speed": speed, "work_mode": workMode, "runtime_state": "desired_not_applied", "vpp_operation": map[string]any{"name": "vpp.interface-bond", "mode": "xor", "load_balance": "l34", "members": members}, "capabilities": server.degradedRuntimeCapabilities()}, http.StatusOK, nil
}

func (server *Server) handleDHCPLeases(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	items, capabilities, err := server.dhcpLeaseSnapshot(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "dhcp_leases_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items), "capabilities": capabilities, "request_id": requestID(r)})
}

func (server *Server) handleDHCPLeaseItem(w http.ResponseWriter, r *http.Request) {
	id, action := dhcpLeasePath(r.URL.Path)
	if id == "" || action != "reserve" {
		writeError(w, r, http.StatusNotFound, "not_found", "resource was not found")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	session, ok := server.sessionFromRequest(r)
	if !ok {
		server.recordAudit("anonymous", "system", r.URL.Path, "reserve", "denied", "authentication required", r)
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	if session.Role != "admin" {
		server.recordAudit(session.Username, session.Role, r.URL.Path, "reserve", "denied", "readonly mutation denied", r)
		writeError(w, r, http.StatusForbidden, "forbidden", "admin role required")
		return
	}
	if server.passwordChangeRequired(w, r, session, r.URL.Path, "reserve") {
		return
	}
	if server.store == nil {
		writeError(w, r, http.StatusServiceUnavailable, "store_unavailable", "local store is not configured")
		return
	}
	lease, err := server.dhcpLeaseByID(r.Context(), id)
	if err != nil {
		server.recordAudit(session.Username, session.Role, r.URL.Path, "reserve", "failure", err.Error(), r)
		writeError(w, r, http.StatusInternalServerError, "dhcp_lease_read_failed", err.Error())
		return
	}
	if lease == nil {
		server.recordAudit(session.Username, session.Role, r.URL.Path, "reserve", "failure", "lease not found", r)
		writeError(w, r, http.StatusNotFound, "not_found", "lease was not found")
		return
	}
	binding, status, err := staticBindingFromLease(lease)
	if err != nil {
		server.recordAudit(session.Username, session.Role, r.URL.Path, "reserve", "failure", err.Error(), r)
		writeError(w, r, status, "invalid_dhcp_lease", err.Error())
		return
	}
	raw, hash, err := persistence.MarshalPayload(binding)
	if err != nil {
		server.recordAudit(session.Username, session.Role, r.URL.Path, "reserve", "failure", err.Error(), r)
		writeError(w, r, http.StatusBadRequest, "invalid_payload", "static reservation could not be encoded")
		return
	}
	if err := server.store.SaveConfig(r.Context(), persistence.ConfigDocument{ResourceType: "dhcp_static_binding", ResourceID: stringField(binding, "id"), Payload: raw, PayloadHash: hash, UpdatedAt: server.now().UTC()}); err != nil {
		server.recordAudit(session.Username, session.Role, r.URL.Path, "reserve", "failure", err.Error(), r)
		writeError(w, r, http.StatusInternalServerError, "static_reservation_save_failed", "static reservation could not be saved")
		return
	}
	server.recordAudit(session.Username, session.Role, r.URL.Path, "reserve", "success", "", r)
	writeJSON(w, http.StatusOK, map[string]any{"item": binding, "request_id": requestID(r)})
}

func dhcpLeasePath(path string) (string, string) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(path, "/api/v1/dhcp/leases/"), "/"), "/")
	if len(parts) != 2 {
		return "", ""
	}
	id, err := url.PathUnescape(parts[0])
	if err != nil {
		return "", ""
	}
	return id, parts[1]
}

func (server *Server) dhcpLeaseByID(ctx context.Context, id string) (map[string]any, error) {
	items, _, err := server.dhcpLeaseSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		for _, key := range []string{"id", "ip_address", "address", "ip", "mac", "hw_address"} {
			if stringField(item, key) == id {
				return item, nil
			}
		}
	}
	return nil, nil
}

func staticBindingFromLease(lease map[string]any) (map[string]any, int, error) {
	ip := firstStringField(lease, "ip_address", "address", "ip")
	mac := firstStringField(lease, "mac", "hw_address", "hwaddr")
	if ip == "" || mac == "" {
		return nil, http.StatusUnprocessableEntity, fmt.Errorf("lease requires IP and MAC to reserve")
	}
	id := strings.TrimSpace(nonEmpty(stringField(lease, "id"), strings.ReplaceAll(mac, ":", "")))
	return map[string]any{"id": id, "kind": "dhcp_static_binding", "ip": ip, "ip_address": ip, "mac": mac, "hostname": firstStringField(lease, "hostname", "client_hostname", "name"), "lease_id": stringField(lease, "id"), "enabled": true, "runtime_state": "desired_not_applied"}, http.StatusOK, nil
}

func (server *Server) compileDNSPolicyResource(ctx context.Context, req DNSPolicyResource) (DNSPolicyResource, json.RawMessage, string, error) {
	if strings.TrimSpace(req.ID) == "" {
		return DNSPolicyResource{}, nil, "", fmt.Errorf("dns policy id is required")
	}
	priority := normalizedDNSPolicyPriority(req.Priority)
	if priority < 1 || priority > 65535 {
		return DNSPolicyResource{}, nil, "", fmt.Errorf("dns policy priority must be between 1 and 65535")
	}
	policy := req.Policy
	if strings.TrimSpace(policy.Engine) == "" && len(policy.Rules) == 0 && policy.Miss.Kind == "" {
		policy = dns.NewPolicy(dns.Reject(), []dns.Rule{{ID: req.ID, Domains: []string{}, Outcome: dns.Direct()}})
	}
	expandedPolicy, err := server.expandDNSPolicySourceGroups(ctx, policy)
	if err != nil {
		return DNSPolicyResource{}, nil, "", err
	}
	compiled, err := dns.CompilePolicy(expandedPolicy, []proxy.Egress{server.proxyEgress})
	if err != nil {
		return DNSPolicyResource{}, nil, "", err
	}
	domainSets, err := server.currentDNSDomainSetsFor(ctx, []map[string]any{{"policy": policy}})
	if err != nil {
		return DNSPolicyResource{}, nil, "", err
	}
	for _, setID := range compiled.ReferencedDomainSetIDs {
		if _, exists := domainSets[setID]; !exists {
			return DNSPolicyResource{}, nil, "", fmt.Errorf("dns policy references unavailable domain set %q", setID)
		}
	}
	payload, hash, err := persistence.MarshalPayload(policy)
	if err != nil {
		return DNSPolicyResource{}, nil, "", err
	}
	return DNSPolicyResource{ID: req.ID, Kind: "policy", Name: nonEmpty(req.Name, req.ID), Priority: priority, Enabled: req.Enabled, Policy: policy, Render: compiled.RenderSmartDNS(), Capabilities: []controlapi.CapabilityState{server.serviceCapability(context.Background(), serviceRuntime.SmartDNS, "smartdns", "SmartDNS service runtime is not configured")}}, payload, hash, nil
}

func (server *Server) dnsPolicyResource(ctx context.Context, id, name string, enabled bool, payload json.RawMessage) (DNSPolicyResource, error) {
	var policy dns.Policy
	if err := json.Unmarshal(payload, &policy); err != nil {
		return DNSPolicyResource{}, err
	}
	expandedPolicy, err := server.expandDNSPolicySourceGroups(ctx, policy)
	if err != nil {
		return DNSPolicyResource{}, err
	}
	compiled, err := dns.CompilePolicy(expandedPolicy, []proxy.Egress{server.proxyEgress})
	if err != nil {
		return DNSPolicyResource{}, err
	}
	return DNSPolicyResource{ID: id, Kind: "policy", Name: nonEmpty(name, id), Enabled: enabled, Policy: policy, Render: compiled.RenderSmartDNS(), Capabilities: []controlapi.CapabilityState{server.serviceCapability(context.Background(), serviceRuntime.SmartDNS, "smartdns", "SmartDNS service runtime is not configured")}}, nil
}

func (server *Server) expandDNSPolicySourceGroups(ctx context.Context, policy dns.Policy) (dns.Policy, error) {
	items, err := server.desiredItems(ctx, "object_group")
	if err != nil {
		return dns.Policy{}, err
	}
	consumer := map[string]any{"policy": policy}
	items, err = materializeObjectGroupItems(items, referencedObjectGroupIDs(items, []map[string]any{consumer}))
	if err != nil {
		return dns.Policy{}, err
	}
	expanded := policy
	expanded.Rules = append([]dns.Rule(nil), policy.Rules...)
	for index := range expanded.Rules {
		rule := &expanded.Rules[index]
		if len(rule.SourcePrefixes) == 0 {
			continue
		}
		selectors, expandErr := trafficpolicy.ExpandAddressSelectors(rule.SourcePrefixes, items)
		if expandErr != nil {
			return dns.Policy{}, fmt.Errorf("dns rule %q source selectors: %w", rule.ID, expandErr)
		}
		rule.SourcePrefixes = selectors
	}
	return expanded, nil
}

// expandDNSPolicyForDecision produces the executable decision view used by
// the diagnostic/preview endpoint. Runtime SmartDNS keeps domain-set IDs in
// its rendered configuration, but the pure policy evaluator needs the actual
// domain selectors to make the same first-match decision as SmartDNS.
func (server *Server) expandDNSPolicyForDecision(ctx context.Context, policy dns.Policy) (dns.Policy, error) {
	expanded, err := server.expandDNSPolicySourceGroups(ctx, policy)
	if err != nil {
		return dns.Policy{}, err
	}
	sets, err := server.currentDNSDomainSets(ctx)
	if err != nil {
		return dns.Policy{}, err
	}
	for index := range expanded.Rules {
		rule := &expanded.Rules[index]
		for _, setID := range rule.DomainSetIDs {
			setID = strings.TrimSpace(setID)
			entries, exists := sets[setID]
			if !exists {
				return dns.Policy{}, fmt.Errorf("dns policy references unavailable domain set %q", setID)
			}
			for _, entry := range entries {
				entry = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(entry)), ".")
				if entry == "" {
					continue
				}
				if strings.HasPrefix(entry, "*.") {
					rule.DomainSuffixes = append(rule.DomainSuffixes, strings.TrimPrefix(entry, "*."))
					continue
				}
				if strings.HasPrefix(entry, ".") {
					rule.DomainSuffixes = append(rule.DomainSuffixes, strings.TrimPrefix(entry, "."))
					continue
				}
				rule.Domains = append(rule.Domains, entry)
			}
		}
	}
	return expanded, nil
}

type desiredResourceDef struct {
	CollectionPath string
	Defaults       []map[string]any
}

var desiredResourceDefs = map[string]desiredResourceDef{
	"interface": {CollectionPath: "/api/v1/interfaces", Defaults: []map[string]any{}},
	"management_network": {CollectionPath: "/api/v1/management/network", Defaults: []map[string]any{
		{"id": "management-network", "interface_id": "eth0", "mode": "exclusive", "cidr": "192.168.88.1/24", "gateway": "", "dhcp_enabled": true, "preserve_management_port": true, "runtime_state": "desired_not_applied"},
	}},
	"interface_bond": {CollectionPath: "/api/v1/interface-bonds", Defaults: []map[string]any{}},
	"object_group": {CollectionPath: "/api/v1/objects/ip-groups", Defaults: []map[string]any{
		{"id": "obj-local-lan", "kind": "ip", "name": "Local LAN", "entries": []string{"192.168.88.0/24"}},
		{"id": "obj-geoip-cn", "kind": "ip", "name": "GeoIP 中国大陆", "entries": []string{}, "source": map[string]any{"provider": "v2ray-rules-dat", "format": "geoip", "category": "CN", "file": "geoip.dat", "sha256": "6ba63d75f307d16a81ae09406ddcf2779fa75cb642d4aae59613370d62d33509"}, "source_entry_count": 5822},
		{"id": "obj-geosite-cn", "kind": "domain", "name": "GeoSite 中国大陆", "entries": []string{}, "source": map[string]any{"provider": "v2ray-rules-dat", "format": "geosite", "category": "CN", "file": "geosite.dat", "sha256": "857227f9dcedbfda5c067ba740ca8a461a06a6ac12aeeb99dcbf82c0e1bdb125"}, "source_entry_count": 111514},
		{"id": "obj-proxy-domains", "kind": "domain", "name": "Proxy Domains", "entries": []string{}},
	}},
	"wan_link":           {CollectionPath: "/api/v1/gateway/wan-links", Defaults: []map[string]any{}},
	"wan_group":          {CollectionPath: "/api/v1/gateway/wan-groups", Defaults: []map[string]any{}},
	"route_policy":       {CollectionPath: "/api/v1/gateway/policies/routes", Defaults: []map[string]any{}},
	"nat_static":         {CollectionPath: "/api/v1/gateway/nat/static", Defaults: []map[string]any{}},
	"port_map":           {CollectionPath: "/api/v1/gateway/nat/port-maps", Defaults: []map[string]any{}},
	"proxy_node":         {CollectionPath: "/api/v1/proxy/nodes", Defaults: []map[string]any{}},
	"proxy_subscription": {CollectionPath: "/api/v1/proxy/subscriptions", Defaults: []map[string]any{}},
	"proxy_group":        {CollectionPath: "/api/v1/proxy/groups", Defaults: []map[string]any{{"id": "proxy-group-default", "kind": "group", "name": "Default Proxy Group", "enabled": true, "members": []string{"proxy-egress-default"}, "degraded": true}}},
	"dns_policy":         {CollectionPath: "/api/v1/dns/policies", Defaults: []map[string]any{{"id": "default", "kind": "policy", "name": "Default DNS Policy", "enabled": true, "policy": map[string]any{"engine": "smartdns", "miss": map[string]any{"kind": "reject"}, "rules": []any{}}, "degraded": true}}},
	"domain_ip_set":      {CollectionPath: "/api/v1/dns/domain-ip-sets", Defaults: []map[string]any{}},
	"dns_upstream": {CollectionPath: "/api/v1/dns/upstreams", Defaults: []map[string]any{
		{"id": "dns-direct-default", "name": "Default Direct Resolvers", "engine": "smartdns", "servers": []string{"1.1.1.1", "8.8.8.8"}, "cache_size": 32768, "ttl_min_seconds": 60, "ttl_max_seconds": 600, "prefetch": true, "degraded": true, "degraded_reason": "SmartDNS process manager not wired"},
	}},
	"dhcp_server":             {CollectionPath: "/api/v1/dhcp/servers", Defaults: []map[string]any{}},
	"dhcp_lease":              {CollectionPath: "/api/v1/dhcp/leases", Defaults: []map[string]any{}},
	"dhcp_static_binding":     {CollectionPath: "/api/v1/dhcp/static-bindings", Defaults: []map[string]any{}},
	"security_acl":            {CollectionPath: "/api/v1/security/acls", Defaults: []map[string]any{{"id": "sec-acl-default-deny-wan", "kind": "acl", "name": "Default WAN Inbound Deny", "enabled": true, "match": map[string]any{"enabled": true, "schedule": "always", "src_ip": "any", "dst_ip": "wan", "protocol": "any", "direction": "wan_to_lan"}, "action": "deny", "degraded": true}}},
	"security_ip_mac_binding": {CollectionPath: "/api/v1/security/ip-mac-bindings", Defaults: []map[string]any{}},
	"security_threat_intel":   {CollectionPath: "/api/v1/security/threat-intel", Defaults: []map[string]any{}},
	"security_attack_rule":    {CollectionPath: "/api/v1/security/attack-rules", Defaults: []map[string]any{}},
	"traffic_control":         {CollectionPath: "/api/v1/flow-control/policies", Defaults: []map[string]any{{"id": "default", "rules": []map[string]any{{"id": "classify-default", "granularity": "rule", "actions": []map[string]any{{"kind": "classify", "traffic_class": "best-effort"}}}}, "capabilities": []controlapi.CapabilityState{{Name: "vpp_qos_push", Available: false, State: controlapi.CapabilityDegraded, Reason: "live VPP QoS adapter not wired"}}}}},
}

func defaultDatapathCapability() map[string]any {
	return map[string]any{"state": "dataplane_locked", "vpp_native": "requires_runtime_proof", "rdma_dv": "unknown", "af_xdp_zero_copy": "unknown"}
}

func (server *Server) handleDesiredCollection(resourceType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if resourceType == "interface" {
				items, capabilities, err := server.interfaceRuntimeSnapshot(r.Context())
				if err != nil {
					writeError(w, r, http.StatusInternalServerError, "interface_snapshot_failed", err.Error())
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{"items": items, "capabilities": capabilities, "request_id": requestID(r)})
				return
			}
			items, err := server.desiredItems(r.Context(), resourceType)
			if err != nil {
				writeError(w, r, http.StatusInternalServerError, "desired_state_read_failed", err.Error())
				return
			}
			if resourceType == "port_map" {
				items = decoratePortMapItems(items)
			}
			if resourceType == "route_policy" {
				items = server.decorateRoutePolicyRuntimeStates(r.Context(), items)
			}
			if resourceType == "object_group" && server.profile.ID() == product.Orchestrator().ID() {
				filtered := make([]map[string]any, 0, len(items))
				for _, item := range items {
					if objectGroupKind(item) == "ip" {
						filtered = append(filtered, item)
					}
				}
				items = filtered
			}
			if resourceType == "traffic_control" {
				items = decorateTrafficControlItems(items)
			}
			writeJSON(w, http.StatusOK, map[string]any{"items": items, "capabilities": server.degradedRuntimeCapabilities(), "request_id": requestID(r)})
		case http.MethodPost:
			server.handleDesiredMutation(w, r, resourceType, "create", "")
		default:
			writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		}
	}
}

func (server *Server) handleDesiredItem(resourceType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, action := desiredPathID(resourceType, r.URL.Path)
		if id == "" {
			writeError(w, r, http.StatusNotFound, "not_found", "resource was not found")
			return
		}
		if action == "stats" && r.Method == http.MethodGet {
			item, ok, capabilities, err := server.interfaceRuntimeItem(r.Context(), id)
			if err != nil {
				writeError(w, r, http.StatusInternalServerError, "interface_snapshot_failed", err.Error())
				return
			}
			if !ok {
				writeError(w, r, http.StatusNotFound, "not_found", "resource was not found")
				return
			}
			if resourceType == "object_group" && server.profile.ID() == product.Orchestrator().ID() && objectGroupKind(item) != "ip" {
				writeError(w, r, http.StatusNotFound, "not_found", "resource was not found")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"item": item, "capabilities": capabilities, "id": id, "request_id": requestID(r)})
			return
		}
		if resourceType == "object_group" && action == "export" && r.Method == http.MethodGet {
			server.handleObjectGroupExport(w, r, id)
			return
		}
		if resourceType == "object_group" && action == "import" && r.Method == http.MethodPost {
			server.handleObjectGroupImport(w, r, id)
			return
		}
		if resourceType == "proxy_subscription" && action == "refresh" && r.Method == http.MethodPost {
			server.handleProxySubscriptionRefresh(w, r, id)
			return
		}
		switch r.Method {
		case http.MethodGet:
			if resourceType == "interface" {
				item, ok, capabilities, err := server.interfaceRuntimeItem(r.Context(), id)
				if err != nil {
					writeError(w, r, http.StatusInternalServerError, "interface_snapshot_failed", err.Error())
					return
				}
				if !ok {
					writeError(w, r, http.StatusNotFound, "not_found", "resource was not found")
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{"item": item, "capabilities": capabilities, "request_id": requestID(r)})
				return
			}
			item, ok, err := server.desiredItem(r.Context(), resourceType, id)
			if err != nil {
				writeError(w, r, http.StatusInternalServerError, "desired_state_read_failed", err.Error())
				return
			}
			if !ok {
				writeError(w, r, http.StatusNotFound, "not_found", "resource was not found")
				return
			}
			if resourceType == "port_map" {
				item = decoratePortMapItem(item)
			}
			if resourceType == "route_policy" {
				item = server.decorateRoutePolicyRuntimeState(r.Context(), item)
			}
			if resourceType == "traffic_control" {
				item = decorateTrafficControlItem(item)
			}
			writeJSON(w, http.StatusOK, map[string]any{"item": item, "capabilities": server.degradedRuntimeCapabilities(), "request_id": requestID(r)})
		case http.MethodPatch, http.MethodPost:
			server.handleDesiredMutation(w, r, resourceType, actionOrDefault(action, "update"), id)
		case http.MethodDelete:
			server.handleDesiredDelete(w, r, resourceType, id)
		default:
			writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		}
	}
}

func (server *Server) handleDesiredDelete(w http.ResponseWriter, r *http.Request, resourceType, id string) {
	session, ok := server.sessionFromRequest(r)
	if !ok {
		server.recordAudit("anonymous", "system", r.URL.Path, "delete", "denied", "authentication required", r)
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	if session.Role != "admin" {
		server.recordAudit(session.Username, session.Role, r.URL.Path, "delete", "denied", "readonly mutation denied", r)
		writeError(w, r, http.StatusForbidden, "forbidden", "admin role required")
		return
	}
	if server.passwordChangeRequired(w, r, session, r.URL.Path, "delete") {
		return
	}
	if server.store == nil {
		writeError(w, r, http.StatusServiceUnavailable, "store_unavailable", "local store is not configured")
		return
	}
	if resourceType == "interface" {
		id = server.resolveInterfaceID(r.Context(), id)
		if id == server.managementInterfaceID(r.Context()) {
			server.recordAudit(session.Username, session.Role, r.URL.Path, "delete", "denied", "management interface role is protected", r)
			writeError(w, r, http.StatusUnprocessableEntity, "management_interface_protected", "management interface role cannot be deleted")
			return
		}
	}
	if resourceType == "object_group" {
		references, err := server.objectGroupReferences(r.Context(), id)
		if err != nil {
			server.recordAudit(session.Username, session.Role, r.URL.Path, "delete", "failure", err.Error(), r)
			writeError(w, r, http.StatusInternalServerError, "reference_scan_failed", "object group references could not be scanned")
			return
		}
		if len(references) > 0 {
			server.recordAudit(session.Username, session.Role, r.URL.Path, "delete", "denied", "object group is referenced by enabled policies", r)
			writeJSON(w, http.StatusConflict, map[string]any{"error": map[string]any{"code": "object_group_in_use", "message": "object group is referenced by enabled policies", "request_id": requestID(r), "references": references}})
			return
		}
	}
	if err := server.store.DeleteConfig(r.Context(), resourceType, id); err != nil {
		if errors.Is(err, persistence.ErrNotFound) {
			if resourceType == "proxy_egress" && id == server.proxyEgress.ID {
				payload := map[string]any{"id": id, "deleted": true, "runtime_state": "desired_not_applied"}
				raw, _ := json.Marshal(payload)
				if saveErr := server.store.SaveConfig(r.Context(), persistence.ConfigDocument{ResourceType: resourceType, ResourceID: id, Payload: raw, PayloadHash: hashObject(payload), UpdatedAt: server.now().UTC()}); saveErr != nil {
					server.recordAudit(session.Username, session.Role, r.URL.Path, "delete", "failure", saveErr.Error(), r)
					writeError(w, r, http.StatusInternalServerError, "desired_state_delete_failed", "desired state could not be deleted")
					return
				}
				server.recordAudit(session.Username, session.Role, r.URL.Path, "delete", "success", "", r)
				writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id, "runtime_state": "desired_not_applied", "request_id": requestID(r)})
				return
			}
			server.recordAudit(session.Username, session.Role, r.URL.Path, "delete", "failure", "resource not found", r)
			writeError(w, r, http.StatusNotFound, "not_found", "resource was not found")
			return
		}
		server.recordAudit(session.Username, session.Role, r.URL.Path, "delete", "failure", err.Error(), r)
		writeError(w, r, http.StatusInternalServerError, "desired_state_delete_failed", "desired state could not be deleted")
		return
	}
	server.recordAudit(session.Username, session.Role, r.URL.Path, "delete", "success", "", r)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id, "runtime_state": "desired_not_applied", "request_id": requestID(r)})
}

func (server *Server) objectGroupReferences(ctx context.Context, id string) ([]map[string]any, error) {
	consumers := []string{"route_policy", "security_acl", "nat_static", "port_map", "dns_policy", "traffic_control"}
	var references []map[string]any
	for _, resourceType := range consumers {
		items, err := server.desiredItems(ctx, resourceType)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if !desiredEnabled(item) || !containsReference(item, id) {
				continue
			}
			references = append(references, map[string]any{"resource_type": resourceType, "id": stringField(item, "id"), "name": stringField(item, "name")})
		}
	}
	return references, nil
}

func desiredEnabled(item map[string]any) bool {
	if value, ok := item["enabled"].(bool); ok {
		return value
	}
	return true
}

func containsReference(value any, id string) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "id" || key == "name" {
				continue
			}
			if containsReference(child, id) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsReference(child, id) {
				return true
			}
		}
	case []map[string]any:
		for _, child := range typed {
			if containsReference(child, id) {
				return true
			}
		}
	case []string:
		for _, child := range typed {
			if strings.TrimSpace(child) == id {
				return true
			}
		}
	case string:
		for _, token := range strings.FieldsFunc(typed, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r' }) {
			if strings.TrimSpace(token) == id {
				return true
			}
		}
	}
	return false
}

func (server *Server) handleObjectGroupExport(w http.ResponseWriter, r *http.Request, id string) {
	item, ok, err := server.desiredItem(r.Context(), "object_group", id)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "object_group_read_failed", err.Error())
		return
	}
	if !ok {
		writeError(w, r, http.StatusNotFound, "not_found", "resource was not found")
		return
	}
	if source, hasSource := objectGroupSource(item); hasSource {
		materialized, materializeErr := materializeObjectGroupItems([]map[string]any{item}, map[string]bool{id: true})
		if materializeErr != nil {
			writeError(w, r, http.StatusUnprocessableEntity, "geodata_source_unavailable", materializeErr.Error())
			return
		}
		if len(materialized) == 1 {
			item = materialized[0]
		} else {
			writeError(w, r, http.StatusUnprocessableEntity, "geodata_source_unavailable", fmt.Sprintf("object group %q source %s could not be materialized", id, source.File))
			return
		}
	}
	entries := objectGroupEntries(item)
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "kind": objectGroupKind(item), "entries": entries, "text": strings.Join(entries, "\n"), "request_id": requestID(r)})
}

func (server *Server) handleObjectGroupImport(w http.ResponseWriter, r *http.Request, id string) {
	session, ok := server.sessionFromRequest(r)
	if !ok {
		server.recordAudit("anonymous", "system", r.URL.Path, "import", "denied", "authentication required", r)
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	if session.Role != "admin" {
		server.recordAudit(session.Username, session.Role, r.URL.Path, "import", "denied", "readonly mutation denied", r)
		writeError(w, r, http.StatusForbidden, "forbidden", "admin role required")
		return
	}
	if server.passwordChangeRequired(w, r, session, r.URL.Path, "import") {
		return
	}
	if server.store == nil {
		writeError(w, r, http.StatusServiceUnavailable, "store_unavailable", "local store is not configured")
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	importMode := "append"
	var importedSource *geodata.Source
	importKind := strings.ToLower(strings.TrimSpace(r.FormValue("kind")))
	if importKind == "" {
		importKind = "ip"
	}
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data") {
		if err := r.ParseMultipartForm(64 << 20); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_multipart", "uploaded object-group file could not be read")
			return
		}
		importMode = strings.ToLower(strings.TrimSpace(r.FormValue("mode")))
		if importMode == "" {
			importMode = "append"
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "file_required", "an object-group import file is required")
			return
		}
		raw, readErr := io.ReadAll(io.LimitReader(file, 64<<20))
		_ = file.Close()
		if readErr != nil {
			writeError(w, r, http.StatusBadRequest, "file_read_failed", "uploaded object-group file could not be read")
			return
		}
		format := strings.ToLower(strings.TrimSpace(r.FormValue("format")))
		if format == "" {
			switch strings.ToLower(filepath.Ext(header.Filename)) {
			case ".dat":
				format = "geosite"
			default:
				format = "text"
			}
		}
		category := strings.TrimSpace(r.FormValue("category"))
		if format == "geoip" || format == "geosite" {
			parsed, parseErr := func() (geodata.Data, error) {
				if format == "geoip" {
					return geodata.ParseGeoIP(raw, category)
				}
				return geodata.ParseGeoSite(raw, category)
			}()
			if parseErr != nil {
				writeError(w, r, http.StatusUnprocessableEntity, "geodata_import_failed", parseErr.Error())
				return
			}
			req.Text = strings.Join(parsed.Entries, "\n")
			importedSource = &geodata.Source{Format: format, Category: parsed.Category, File: header.Filename, EntryCount: len(parsed.Entries)}
		} else {
			textData, parseErr := geodata.ParseText(raw, importKind)
			if parseErr != nil {
				writeError(w, r, http.StatusUnprocessableEntity, "text_import_failed", parseErr.Error())
				return
			}
			req.Text = strings.Join(textData.Entries, "\n")
		}
	} else if err := decodeStrictJSON(r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
		return
	}
	item, ok, err := server.desiredItem(r.Context(), "object_group", id)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "object_group_read_failed", err.Error())
		return
	}
	if !ok {
		writeError(w, r, http.StatusNotFound, "not_found", "resource was not found")
		return
	}
	// A source-backed built-in group is intentionally lazy in list responses.
	// Once a user imports or appends manual entries, materialize the source once
	// and remove the lazy source marker so the edited group is authoritative.
	if _, hasSource := objectGroupSource(item); hasSource {
		if importMode != "overwrite" && importMode != "replace" {
			materialized, materializeErr := materializeObjectGroupItems([]map[string]any{item}, map[string]bool{id: true})
			if materializeErr != nil {
				writeError(w, r, http.StatusUnprocessableEntity, "geodata_source_unavailable", materializeErr.Error())
				return
			}
			if len(materialized) == 1 {
				item = materialized[0]
			}
		}
		delete(item, "source")
	}
	originalEntries := objectGroupEntries(item)
	baseEntries := originalEntries
	if importMode == "overwrite" || importMode == "replace" {
		baseEntries = nil
	}
	entries, invalidLines := importObjectGroupEntries(objectGroupKind(item), baseEntries, req.Text)
	if len(entries) > 0 {
		item["entries"] = entries
		item["members"] = entries
		item["source_entry_count"] = len(entries)
		item["compiled_expansion"] = map[string]any{"kind": objectGroupKind(item), "entries": entries, "entry_count": len(entries)}
		affected, _ := server.objectGroupReferences(r.Context(), id)
		for _, consumer := range affected {
			consumer["runtime_state"] = "dirty"
			consumer["recompile_state"] = "pending"
		}
		item["affected_consumers"] = affected
		item["runtime_state"] = "desired_not_applied"
		if importedSource != nil {
			item["import_source"] = importedSource
		}
		item["capabilities"] = server.degradedRuntimeCapabilities()
		raw, hash, err := persistence.MarshalPayload(item)
		if err != nil {
			server.recordAudit(session.Username, session.Role, r.URL.Path, "import", "failure", err.Error(), r)
			writeError(w, r, http.StatusBadRequest, "invalid_payload", "object group payload could not be encoded")
			return
		}
		if err := server.store.SaveConfig(r.Context(), persistence.ConfigDocument{ResourceType: "object_group", ResourceID: id, Payload: raw, PayloadHash: hash, UpdatedAt: server.now().UTC()}); err != nil {
			server.recordAudit(session.Username, session.Role, r.URL.Path, "import", "failure", err.Error(), r)
			writeError(w, r, http.StatusInternalServerError, "object_group_import_failed", "object group could not be saved")
			return
		}
	}
	server.recordAudit(session.Username, session.Role, r.URL.Path, "import", "success", "", r)
	importedCount := len(entries)
	if importMode != "overwrite" && importMode != "replace" && len(entries) >= len(originalEntries) {
		importedCount = len(entries) - len(originalEntries)
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "imported_count": importedCount, "entries": entries, "invalid_lines": invalidLines, "compiled_expansion": item["compiled_expansion"], "affected_consumers": item["affected_consumers"], "runtime_state": "desired_not_applied", "request_id": requestID(r)})
}

func objectGroupKind(item map[string]any) string {
	return firstStringField(item, "group_type", "type", "kind")
}

func (server *Server) normalizeObjectGroupPayload(ctx context.Context, payload map[string]any) error {
	kind := objectGroupKind(payload)
	if kind != "ip" && kind != "domain" {
		return fmt.Errorf("object_group kind must be ip or domain")
	}
	if server.profile.ID() == product.Orchestrator().ID() && kind != "ip" {
		return fmt.Errorf("Orchestrator supports IP groups only")
	}
	name := stringField(payload, "name")
	if name == "" {
		return fmt.Errorf("object_group name is required")
	}
	if server.store != nil {
		groups, err := server.desiredItems(ctx, "object_group")
		if err != nil {
			return err
		}
		id := stringField(payload, "id")
		for _, group := range groups {
			if stringField(group, "id") == id {
				continue
			}
			if objectGroupKind(group) == kind && strings.EqualFold(stringField(group, "name"), name) {
				return fmt.Errorf("object_group name %q already exists for kind %s", name, kind)
			}
		}
	}
	entries, err := canonicalObjectGroupEntries(kind, objectGroupEntries(payload))
	if err != nil {
		return err
	}
	payload["kind"] = kind
	payload["type"] = kind
	payload["entries"] = entries
	payload["members"] = entries
	payload["compiled_expansion"] = map[string]any{"kind": kind, "entries": entries, "entry_count": len(entries)}
	affected, _ := server.objectGroupReferences(ctx, stringField(payload, "id"))
	for _, consumer := range affected {
		consumer["runtime_state"] = "dirty"
		consumer["recompile_state"] = "pending"
	}
	payload["affected_consumers"] = affected
	return nil
}

func canonicalObjectGroupEntries(kind string, raw []string) ([]string, error) {
	seen := map[string]struct{}{}
	entries := make([]string, 0, len(raw))
	for _, entry := range raw {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if err := validateObjectGroupEntry(kind, entry); err != nil {
			return nil, fmt.Errorf("object_group entry %q invalid: %w", entry, err)
		}
		if _, ok := seen[entry]; ok {
			continue
		}
		seen[entry] = struct{}{}
		entries = append(entries, entry)
	}
	return entries, nil
}

func objectGroupEntries(item map[string]any) []string {
	return append(stringSliceField(item, "entries"), stringSliceField(item, "members")...)
}

// objectGroupSource is deliberately metadata-only in the public object-group
// response.  Large GeoSite categories can contain more than one hundred
// thousand selectors; expanding them in every list response makes the UI
// slow and needlessly increases the persistence/API surface.  Runtime
// compilation calls materializeObjectGroupItems for only referenced groups.
func objectGroupSource(item map[string]any) (geodata.Source, bool) {
	raw, ok := item["source"].(map[string]any)
	if !ok || len(raw) == 0 {
		return geodata.Source{}, false
	}
	source := geodata.Source{
		Format:   stringField(raw, "format"),
		Category: stringField(raw, "category"),
		File:     stringField(raw, "file"),
		SHA256:   stringField(raw, "sha256"),
	}
	return source, source.Format != "" && source.Category != ""
}

func materializeObjectGroupItems(items []map[string]any, required map[string]bool) ([]map[string]any, error) {
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		clone := cloneObject(item)
		id := stringField(clone, "id")
		source, hasSource := objectGroupSource(clone)
		if hasSource && (required == nil || required[id]) {
			data, err := geodata.LoadSource(source)
			if err != nil {
				return nil, fmt.Errorf("object group %q: %w", id, err)
			}
			if objectGroupKind(clone) == "ip" && data.Format != geodata.FormatGeoIP {
				return nil, fmt.Errorf("object group %q requires a GeoIP source", id)
			}
			if objectGroupKind(clone) == "domain" && data.Format != geodata.FormatGeoSite {
				return nil, fmt.Errorf("object group %q requires a GeoSite source", id)
			}
			clone["entries"] = append([]string(nil), data.Entries...)
			clone["members"] = append([]string(nil), data.Entries...)
			clone["source_entry_count"] = len(data.Entries)
			if sourceMap, ok := clone["source"].(map[string]any); ok {
				sourceMap["sha256"] = data.SourceSHA256
				sourceMap["entry_count"] = len(data.Entries)
			}
		}
		result = append(result, clone)
	}
	return result, nil
}

func referencedObjectGroupIDs(groups, consumers []map[string]any) map[string]bool {
	required := make(map[string]bool)
	for _, group := range groups {
		id := stringField(group, "id")
		if id == "" || !containsReference(consumers, id) {
			continue
		}
		required[id] = true
	}
	return required
}

func importObjectGroupEntries(kind string, existing []string, text string) ([]string, []map[string]any) {
	seen := map[string]struct{}{}
	entries := make([]string, 0, len(existing))
	for _, entry := range existing {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if _, ok := seen[entry]; ok {
			continue
		}
		seen[entry] = struct{}{}
		entries = append(entries, entry)
	}
	var invalid []map[string]any
	for index, line := range strings.Split(text, "\n") {
		entry := strings.TrimSpace(line)
		if entry == "" || strings.HasPrefix(entry, "#") {
			continue
		}
		if err := validateObjectGroupEntry(kind, entry); err != nil {
			invalid = append(invalid, map[string]any{"line": index + 1, "value": entry, "error": err.Error()})
			continue
		}
		if _, ok := seen[entry]; ok {
			continue
		}
		seen[entry] = struct{}{}
		entries = append(entries, entry)
	}
	return entries, invalid
}

func validateObjectGroupEntry(kind, entry string) error {
	if strings.ContainsAny(entry, " \t\r\n;|&$()<>\\\"") {
		return fmt.Errorf("entry contains unsupported characters")
	}
	switch kind {
	case "ip":
		if _, err := netip.ParseAddr(entry); err == nil {
			return nil
		}
		if _, err := netip.ParsePrefix(entry); err == nil {
			return nil
		}
		parts := strings.Split(entry, "-")
		if len(parts) == 2 {
			if _, err := netip.ParseAddr(parts[0]); err == nil {
				if _, err := netip.ParseAddr(parts[1]); err == nil {
					return nil
				}
			}
		}
		return fmt.Errorf("entry must be an IP address, CIDR, or IP range")
	case "domain":
		if strings.ContainsAny(entry, "/:*?") || strings.HasPrefix(entry, "-") || strings.HasSuffix(entry, "-") || !strings.Contains(entry, ".") {
			return fmt.Errorf("entry must be an exact or suffix domain")
		}
		return nil
	default:
		return nil
	}
}

func (server *Server) handleProxyEgressItem(w http.ResponseWriter, r *http.Request) {
	id, _ := splitPathRemainder("/api/v1/proxy/egresses/", r.URL.Path)
	if id == "" {
		writeError(w, r, http.StatusNotFound, "not_found", "resource was not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		items, err := server.proxyEgressResources(r.Context())
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "proxy_egress_read_failed", err.Error())
			return
		}
		for _, item := range items {
			if item.ID == id {
				writeJSON(w, http.StatusOK, map[string]any{"item": item, "request_id": requestID(r)})
				return
			}
		}
		writeError(w, r, http.StatusNotFound, "not_found", "resource was not found")
	case http.MethodPatch:
		server.handleDesiredMutation(w, r, "proxy_egress", "update", id)
	case http.MethodDelete:
		server.handleDesiredDelete(w, r, "proxy_egress", id)
	default:
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
	}
}

func actionOrDefault(action, fallback string) string {
	if strings.TrimSpace(action) == "" {
		return fallback
	}
	return action
}

func desiredPathID(resourceType, path string) (string, string) {
	def, ok := desiredResourceDefs[resourceType]
	if !ok {
		return "", ""
	}
	if resourceType == "traffic_control" && strings.HasPrefix(path, "/api/v1/gateway/traffic-control/") {
		return splitPathRemainder("/api/v1/gateway/traffic-control/", path)
	}
	if resourceType == "object_group" && strings.HasPrefix(path, "/api/v1/objects/groups/") {
		return splitPathRemainder("/api/v1/objects/groups/", path)
	}
	return splitPathRemainder(def.CollectionPath+"/", path)
}

func splitPathRemainder(prefix, path string) (string, string) {
	rest := strings.Trim(strings.TrimPrefix(path, prefix), "/")
	if rest == "" || strings.Contains(rest, "..") {
		return "", ""
	}
	parts := strings.Split(rest, "/")
	if len(parts) == 1 {
		return parts[0], ""
	}
	return parts[0], parts[1]
}

func (server *Server) desiredItems(ctx context.Context, resourceType string) ([]map[string]any, error) {
	def := desiredResourceDefs[resourceType]
	items := map[string]map[string]any{}
	for index, item := range def.Defaults {
		clone := cloneObject(item)
		applyFactoryLANInterface(resourceType, clone)
		id := stringField(clone, "id")
		if id == "" {
			id = fmt.Sprintf("%s-%d", resourceType, index+1)
			clone["id"] = id
		}
		items[id] = clone
	}
	if server.store != nil {
		documents, err := server.store.Configs(ctx, resourceType)
		if err != nil {
			return nil, err
		}
		for _, document := range documents {
			var item map[string]any
			if err := json.Unmarshal(document.Payload, &item); err != nil {
				return nil, err
			}
			if stringField(item, "id") == "" {
				item["id"] = document.ResourceID
			}
			items[document.ResourceID] = item
		}
	}
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if resourceType == "object_group" && server.profile.ID().String() == "orchestrator" && objectGroupKind(item) != "ip" {
			continue
		}
		result = append(result, item)
	}
	sort.Slice(result, func(left, right int) bool {
		return stringField(result[left], "id") < stringField(result[right], "id")
	})
	return result, nil
}

func applyFactoryLANInterface(resourceType string, item map[string]any) {
	lanInterface := factoryLANInterface()
	if lanInterface == "eth0" {
		return
	}
	switch resourceType {
	case "interface":
		if stringField(item, "id") == "eth0" {
			item["id"] = lanInterface
		}
		if stringField(item, "name") == "eth0" {
			item["name"] = lanInterface
		}
	case "dhcp_server":
		if stringField(item, "interface_id") == "eth0" {
			item["interface_id"] = lanInterface
		}
	case "management_network":
		if stringField(item, "interface_id") == "eth0" {
			item["interface_id"] = lanInterface
		}
	}
}

func factoryLANInterface() string {
	if value := strings.TrimSpace(os.Getenv("LY_ROUTE_MANAGEMENT_INTERFACE")); value != "" {
		return value
	}
	if value := strings.TrimSpace(os.Getenv("LY_ROUTE_LAN_INTERFACE")); value != "" {
		return value
	}
	return "eth0"
}

func (server *Server) desiredItem(ctx context.Context, resourceType, id string) (map[string]any, bool, error) {
	items, err := server.desiredItems(ctx, resourceType)
	if err != nil {
		return nil, false, err
	}
	for _, item := range items {
		if stringField(item, "id") == id {
			return item, true, nil
		}
	}
	return nil, false, nil
}

func (server *Server) handleDesiredMutation(w http.ResponseWriter, r *http.Request, resourceType, action, explicitID string) {
	session, ok := server.sessionFromRequest(r)
	if !ok {
		server.recordAudit("anonymous", "system", r.URL.Path, action, "denied", "authentication required", r)
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	if session.Role != "admin" {
		server.recordAudit(session.Username, session.Role, r.URL.Path, action, "denied", "readonly mutation denied", r)
		writeError(w, r, http.StatusForbidden, "forbidden", "admin role required")
		return
	}
	if server.passwordChangeRequired(w, r, session, r.URL.Path, action) {
		return
	}
	if server.store == nil {
		writeError(w, r, http.StatusServiceUnavailable, "store_unavailable", "local store is not configured")
		return
	}
	var payload map[string]any
	if err := decodeStrictJSON(r, &payload); err != nil {
		server.recordAudit(session.Username, session.Role, r.URL.Path, action, "failure", "request body must be valid JSON", r)
		writeError(w, r, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
		return
	}
	if resourceType == "proxy_node" {
		if err := normalizeProxyNodeURIPayload(payload); err != nil {
			server.recordAudit(session.Username, session.Role, r.URL.Path, action, "failure", err.Error(), r)
			writeError(w, r, http.StatusUnprocessableEntity, "invalid_desired_resource", err.Error())
			return
		}
	}
	id := nonEmpty(explicitID, stringField(payload, "id"))
	if id == "" {
		id = strings.ReplaceAll(newRequestID(), "-", "")
	}
	payload["id"] = id
	secrets := extractDesiredSecrets(resourceType, payload)
	if err := validateDesiredPayload(resourceType, payload); err != nil {
		server.recordAudit(session.Username, session.Role, r.URL.Path, action, "failure", err.Error(), r)
		writeError(w, r, http.StatusUnprocessableEntity, "invalid_desired_resource", err.Error())
		return
	}
	if resourceType == "interface" {
		server.normalizeInterfaceDesiredPayload(r.Context(), payload, explicitID)
		id = stringField(payload, "id")
	}
	if resourceType == "wan_link" {
		server.normalizeWANLinkDesiredPayload(r.Context(), payload, explicitID)
		id = stringField(payload, "id")
	}
	if resourceType == "object_group" {
		if err := server.normalizeObjectGroupPayload(r.Context(), payload); err != nil {
			server.recordAudit(session.Username, session.Role, r.URL.Path, action, "failure", err.Error(), r)
			writeError(w, r, http.StatusUnprocessableEntity, "invalid_desired_resource", err.Error())
			return
		}
	}
	if err := server.validateDesiredRuntimePayload(r.Context(), resourceType, action, payload); err != nil {
		server.recordAudit(session.Username, session.Role, r.URL.Path, action, "failure", err.Error(), r)
		writeError(w, r, http.StatusUnprocessableEntity, "invalid_desired_resource", err.Error())
		return
	}
	natRebuild := server.natRebuildImpact(r.Context(), resourceType, id, payload)
	payload["runtime_state"] = "desired_not_applied"
	payload["capabilities"] = server.degradedRuntimeCapabilities()
	raw, hash, err := persistence.MarshalPayload(payload)
	if err != nil {
		server.recordAudit(session.Username, session.Role, r.URL.Path, action, "failure", err.Error(), r)
		writeError(w, r, http.StatusBadRequest, "invalid_payload", "resource payload could not be encoded")
		return
	}
	document := persistence.ConfigDocument{ResourceType: resourceType, ResourceID: id, Payload: raw, PayloadHash: hash, UpdatedAt: server.now().UTC()}
	if err := server.store.SaveConfigWithSecrets(r.Context(), document, secrets); err != nil {
		server.recordAudit(session.Username, session.Role, r.URL.Path, action, "failure", err.Error(), r)
		writeError(w, r, http.StatusInternalServerError, "desired_state_save_failed", "desired state could not be saved")
		return
	}
	server.recordAudit(session.Username, session.Role, r.URL.Path, action, "success", "", r)
	if natRebuild != nil {
		server.recordAudit(session.Username, session.Role, "/api/v1/gateway/nat/port-maps", "nat_rebuild", "success", fmt.Sprintf("wan_link %s address changed; affected_port_maps=%v", id, natRebuild["affected_port_maps"]), r)
	}
	response := map[string]any{"item": payload, "runtime_state": "desired_not_applied", "request_id": requestID(r)}
	if natRebuild != nil {
		response["nat_rebuild"] = natRebuild
	}
	if resourceType == "object_group" {
		response["affected_consumers"] = payload["affected_consumers"]
	}
	writeJSON(w, http.StatusOK, response)
}

func (server *Server) handleManagementNetwork(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost && r.Method != http.MethodPatch {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	session, ok := server.sessionFromRequest(r)
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	if session.Role != "admin" {
		writeError(w, r, http.StatusForbidden, "forbidden", "admin role required")
		return
	}
	if server.passwordChangeRequired(w, r, session, r.URL.Path, strings.ToLower(r.Method)) {
		return
	}
	if server.store == nil {
		writeError(w, r, http.StatusServiceUnavailable, "store_unavailable", "local store is not configured")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, server.managementNetworkState(r.Context(), true))
	case http.MethodPost, http.MethodPatch:
		var payload map[string]any
		if err := decodeStrictJSON(r, &payload); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
			return
		}
		item, err := server.saveManagementNetwork(r.Context(), payload)
		if err != nil {
			writeError(w, r, http.StatusUnprocessableEntity, "invalid_management_network", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"item": item, "runtime_state": "desired_not_applied", "request_id": requestID(r)})
	}
}

func (server *Server) managementNetworkState(ctx context.Context, includeCurrent bool) map[string]any {
	interfaceID := factoryLANInterface()
	cidr := "192.168.88.1/24"
	gateway := ""
	if server.store != nil {
		if documents, err := server.store.Configs(ctx, "management_network"); err == nil && len(documents) > 0 {
			var payload map[string]any
			if json.Unmarshal(documents[0].Payload, &payload) == nil {
				if value := strings.TrimSpace(stringField(payload, "interface_id")); value != "" {
					interfaceID = value
				}
				if value := strings.TrimSpace(nonEmpty(stringField(payload, "cidr"), stringField(payload, "ip_cidr"))); value != "" {
					cidr = value
				}
				gateway = strings.TrimSpace(stringField(payload, "gateway"))
				mode := normalizeManagementMode(stringField(payload, "mode"))
				state := map[string]any{"id": "management-network", "interface_id": interfaceID, "mode": mode, "cidr": cidr, "gateway": gateway, "management_ip": strings.SplitN(cidr, "/", 2)[0], "dhcp_enabled": true, "preserve_management_port": true, "runtime_state": "desired_not_applied", "current": includeCurrent}
				for _, key := range []string{"dhcp_pool_start", "dhcp_pool_end", "dns", "new_url", "rollback_guidance"} {
					if value := strings.TrimSpace(stringField(payload, key)); value != "" {
						state[key] = value
					}
				}
				return state
			}
		}
	}
	return map[string]any{"id": "management-network", "interface_id": interfaceID, "mode": "exclusive", "cidr": cidr, "gateway": gateway, "management_ip": strings.SplitN(cidr, "/", 2)[0], "dhcp_enabled": true, "preserve_management_port": true, "runtime_state": "desired_not_applied", "current": includeCurrent}
}

func normalizeManagementMode(value string) string {
	mode := strings.ToLower(strings.TrimSpace(value))
	if mode == "shared_lan" {
		return mode
	}
	return "exclusive"
}

func (server *Server) managementNetworkShared(ctx context.Context) bool {
	return normalizeManagementMode(stringField(server.managementNetworkState(ctx, false), "mode")) == "shared_lan"
}

func (server *Server) managementInterfaceID(ctx context.Context) string {
	return strings.TrimSpace(stringField(server.managementNetworkState(ctx, false), "interface_id"))
}

func (server *Server) runtimeDataInterfaces(ctx context.Context) []string {
	managementInterface := server.managementInterfaceID(ctx)
	managementShared := server.managementNetworkShared(ctx)
	dataInterfaces := []string{}
	seen := map[string]bool{}
	appendInterface := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || (!managementShared && name == managementInterface) || seen[name] || !vppInterfaceNameSafe(name) {
			return
		}
		seen[name] = true
		dataInterfaces = append(dataInterfaces, name)
	}
	for _, resourceType := range []string{"interface", "wan_link"} {
		items, err := server.desiredItems(ctx, resourceType)
		if err != nil {
			continue
		}
		for _, item := range items {
			if truthy(item["deleted"]) {
				continue
			}
			role := strings.ToLower(strings.TrimSpace(nonEmpty(stringField(item, "gateway_role"), stringField(item, "role"))))
			if !oneOf(role, []string{"lan", "wan", "internal", "external"}) {
				continue
			}
			name := server.resolveInterfaceID(ctx, firstNonEmpty(stringField(item, "interface_id"), stringField(item, "id"), stringField(item, "name")))
			appendInterface(name)
		}
	}
	if server.orchestratorRepository != nil {
		if topology, _, err := server.orchestratorRepository.Snapshot(ctx); err == nil {
			view := topology.View()
			for _, logical := range view.Interfaces {
				if logical.Bond != nil {
					for _, member := range logical.Bond.Members {
						appendInterface(member)
					}
					continue
				}
				appendInterface(logical.Port)
			}
			for _, group := range view.Groups {
				for _, port := range group.Ports {
					appendInterface(port.Interface)
				}
			}
		}
	}
	return dataInterfaces
}

func (server *Server) runtimeAddressAssignments(ctx context.Context) ([]vpp.AddressAssignment, error) {
	assignments := []vpp.AddressAssignment{}
	wanItems, err := server.desiredItems(ctx, "wan_link")
	if err != nil {
		return nil, err
	}
	dhcpWANs := map[string]map[string]any{}
	removeStaticCIDRs := map[string][]string{}
	for _, item := range wanItems {
		if truthy(item["deleted"]) {
			continue
		}
		linuxInterface := server.resolveInterfaceID(ctx, firstNonEmpty(stringField(item, "interface_id"), stringField(item, "id"), stringField(item, "name")))
		if linuxInterface == "" || (!server.managementNetworkShared(ctx) && linuxInterface == server.managementInterfaceID(ctx)) || !vppInterfaceNameSafe(linuxInterface) {
			continue
		}
		if wanDesiredIPv4Mode(item) == "dhcp4" {
			dhcpWANs[linuxInterface] = item
			continue
		}
		if cidr := wanStaticCIDR(item); cidr != "" {
			if parsedCIDR, ok := vppAddressCIDR(cidr); ok {
				removeStaticCIDRs[linuxInterface] = appendUniqueString(removeStaticCIDRs[linuxInterface], parsedCIDR)
			}
		}
	}
	interfaceItems, err := server.desiredItems(ctx, "interface")
	if err != nil {
		return nil, err
	}
	for _, item := range interfaceItems {
		if truthy(item["deleted"]) {
			continue
		}
		cidr := strings.TrimSpace(firstNonEmpty(stringField(item, "cidr"), stringField(item, "ip_cidr"), stringField(item, "ip")))
		if cidr == "" || !strings.Contains(cidr, "/") || stringField(item, "kind") == "lan_bridge" || stringField(item, "type") == "lan_bridge" {
			continue
		}
		parsedCIDR, ok := vppAddressCIDR(cidr)
		if !ok {
			continue
		}
		linuxInterface := server.resolveInterfaceID(ctx, firstNonEmpty(stringField(item, "interface_id"), stringField(item, "id"), stringField(item, "name")))
		if linuxInterface == "" || (!server.managementNetworkShared(ctx) && linuxInterface == server.managementInterfaceID(ctx)) || !vppInterfaceNameSafe(linuxInterface) {
			continue
		}
		if _, ok := dhcpWANs[linuxInterface]; ok {
			removeStaticCIDRs[linuxInterface] = appendUniqueString(removeStaticCIDRs[linuxInterface], parsedCIDR)
			continue
		}
		role := stringField(item, "gateway_role")
		assignments = append(assignments, vpp.AddressAssignment{ID: nonEmpty(stringField(item, "id"), linuxInterface), LinuxInterface: linuxInterface, VPPInterface: vppInterfaceName(linuxInterface), CIDR: parsedCIDR, Role: role, BandwidthKbps: smartQoSBandwidthKbps(item, role)})
	}
	for _, item := range wanItems {
		if truthy(item["deleted"]) || wanDesiredIPv4Mode(item) == "dhcp4" {
			continue
		}
		cidr := wanStaticCIDR(item)
		if cidr == "" {
			continue
		}
		parsedCIDR, ok := vppAddressCIDR(cidr)
		if !ok {
			continue
		}
		linuxInterface := server.resolveInterfaceID(ctx, firstNonEmpty(stringField(item, "interface_id"), stringField(item, "id"), stringField(item, "name")))
		if linuxInterface == "" || (!server.managementNetworkShared(ctx) && linuxInterface == server.managementInterfaceID(ctx)) || !vppInterfaceNameSafe(linuxInterface) {
			continue
		}
		if _, ok := dhcpWANs[linuxInterface]; ok {
			removeStaticCIDRs[linuxInterface] = appendUniqueString(removeStaticCIDRs[linuxInterface], parsedCIDR)
			continue
		}
		assignments = append(assignments, vpp.AddressAssignment{ID: nonEmpty(stringField(item, "id"), linuxInterface), LinuxInterface: linuxInterface, VPPInterface: vppInterfaceName(linuxInterface), CIDR: parsedCIDR, Role: "wan", BandwidthKbps: smartQoSBandwidthKbps(item, "wan")})
	}
	for linuxInterface, item := range dhcpWANs {
		assignments = append(assignments, vpp.AddressAssignment{ID: nonEmpty(stringField(item, "id"), linuxInterface), LinuxInterface: linuxInterface, VPPInterface: vppInterfaceName(linuxInterface), Mode: "dhcp4", RemoveCIDRs: removeStaticCIDRs[linuxInterface], Role: "wan", BandwidthKbps: smartQoSBandwidthKbps(item, "wan")})
	}
	if server.managementNetworkShared(ctx) {
		management := server.managementNetworkState(ctx, false)
		linuxInterface := server.resolveInterfaceID(ctx, stringField(management, "interface_id"))
		cidr := strings.TrimSpace(stringField(management, "cidr"))
		if linuxInterface != "" && cidr != "" {
			if parsedCIDR, ok := vppAddressCIDR(cidr); ok {
				alreadyAssigned := false
				for _, assignment := range assignments {
					if assignment.LinuxInterface == linuxInterface && assignment.CIDR == parsedCIDR {
						alreadyAssigned = true
						break
					}
				}
				if !alreadyAssigned {
					assignments = append(assignments, vpp.AddressAssignment{ID: "management-network", LinuxInterface: linuxInterface, VPPInterface: vppInterfaceName(linuxInterface), CIDR: parsedCIDR, Role: "lan"})
				}
			}
		}
	}
	return assignments, nil
}

// runtimeSmartQoSAssignments maps the user-facing WAN upload/download limits to
// the physical VPP egress interfaces. PPPoE has no WAN address assignment of its
// own, but its encapsulated packets still leave through the underlay interface.
// The aggregate WAN download rate is shaped on the logical LAN egress.
func (server *Server) runtimeSmartQoSAssignments(ctx context.Context, addressAssignments []vpp.AddressAssignment, proxyEgress proxy.Egress, hasProxyEgress bool) ([]vpp.AddressAssignment, error) {
	wanItems, err := server.desiredItems(ctx, "wan_link")
	if err != nil {
		return nil, err
	}

	wanOrder := make([]string, 0, len(addressAssignments)+len(wanItems))
	wanAssignments := map[string]vpp.AddressAssignment{}
	wanInterfaceByID := map[string]string{}
	downloadByWAN := map[string]uint64{}
	for _, assignment := range addressAssignments {
		if !strings.EqualFold(strings.TrimSpace(assignment.Role), "wan") || strings.TrimSpace(assignment.LinuxInterface) == "" {
			continue
		}
		if _, exists := wanAssignments[assignment.LinuxInterface]; !exists {
			wanOrder = append(wanOrder, assignment.LinuxInterface)
		}
		wanAssignments[assignment.LinuxInterface] = assignment
	}
	for _, item := range wanItems {
		if truthy(item["deleted"]) {
			continue
		}
		if enabled, present := item["enabled"]; present && !truthy(enabled) {
			continue
		}
		linuxInterface := server.resolveInterfaceID(ctx, firstNonEmpty(stringField(item, "interface_id"), stringField(item, "id"), stringField(item, "name")))
		if linuxInterface == "" || (!server.managementNetworkShared(ctx) && linuxInterface == server.managementInterfaceID(ctx)) || !vppInterfaceNameSafe(linuxInterface) {
			continue
		}
		if _, exists := wanAssignments[linuxInterface]; !exists {
			wanOrder = append(wanOrder, linuxInterface)
		}
		uploadKbps := smartQoSBandwidthKbps(item, "wan")
		if wanID := firstStringField(item, "id", "name"); wanID != "" {
			wanInterfaceByID[wanID] = linuxInterface
		}
		downloadByWAN[linuxInterface] = smartQoSBandwidthKbps(item, "lan")
		wanAssignments[linuxInterface] = vpp.AddressAssignment{
			ID: nonEmpty(stringField(item, "id"), linuxInterface), LinuxInterface: linuxInterface,
			VPPInterface: vppInterfaceName(linuxInterface), Role: "wan", BandwidthKbps: uploadKbps,
		}
	}
	if hasProxyEgress {
		underlayWANIDs, resolveErr := server.proxyUnderlayWANIDs(ctx, proxyEgress)
		if resolveErr != nil {
			return nil, resolveErr
		}
		for _, wanID := range underlayWANIDs {
			linuxInterface := wanInterfaceByID[wanID]
			if linuxInterface == "" {
				return nil, fmt.Errorf("proxy egress %q underlay WAN %q has no eligible physical VPP interface", proxyEgress.ID, wanID)
			}
			if _, exists := wanAssignments[linuxInterface]; !exists {
				return nil, fmt.Errorf("proxy egress %q underlay WAN %q is not covered by smart QoS", proxyEgress.ID, wanID)
			}
		}
	}

	var aggregateDownloadKbps uint64
	for _, downloadKbps := range downloadByWAN {
		if ^uint64(0)-aggregateDownloadKbps < downloadKbps {
			return nil, fmt.Errorf("aggregate WAN download bandwidth overflows")
		}
		aggregateDownloadKbps += downloadKbps
	}

	assignments := make([]vpp.AddressAssignment, 0, len(addressAssignments)+len(wanOrder))
	seenLAN := map[string]struct{}{}
	for _, assignment := range addressAssignments {
		if !strings.EqualFold(strings.TrimSpace(assignment.Role), "lan") {
			continue
		}
		if _, duplicate := seenLAN[assignment.VPPInterface]; duplicate {
			continue
		}
		seenLAN[assignment.VPPInterface] = struct{}{}
		if aggregateDownloadKbps > 0 {
			assignment.BandwidthKbps = aggregateDownloadKbps
		}
		assignments = append(assignments, assignment)
	}
	for _, linuxInterface := range wanOrder {
		assignments = append(assignments, wanAssignments[linuxInterface])
	}
	return assignments, nil
}

func (server *Server) proxyUnderlayWANIDs(ctx context.Context, egress proxy.Egress) ([]string, error) {
	underlayID := strings.TrimSpace(egress.UnderlayWANID)
	if underlayID == "" {
		// Legacy in-memory callers may omit the field. A persisted/UI-created
		// proxy egress is validated before it reaches runtime; leave selection to
		// the single-WAN fallback below for those callers.
		return nil, nil
	}
	if item, ok, err := server.desiredItem(ctx, "wan_link", underlayID); err != nil {
		return nil, err
	} else if ok {
		if !desiredEnabled(item) {
			return nil, fmt.Errorf("proxy egress %q underlay WAN %q is disabled", egress.ID, underlayID)
		}
		return []string{underlayID}, nil
	}
	if group, ok, err := server.desiredItem(ctx, "wan_group", underlayID); err != nil {
		return nil, err
	} else if ok {
		if !desiredEnabled(group) {
			return nil, fmt.Errorf("proxy egress %q underlay WAN group %q is disabled", egress.ID, underlayID)
		}
		members := wanGroupMemberIDs(group)
		if len(members) == 0 {
			return nil, fmt.Errorf("proxy egress %q underlay WAN group %q has no members", egress.ID, underlayID)
		}
		for _, memberID := range members {
			item, exists, lookupErr := server.desiredItem(ctx, "wan_link", memberID)
			if lookupErr != nil {
				return nil, lookupErr
			}
			if !exists || !desiredEnabled(item) {
				return nil, fmt.Errorf("proxy egress %q underlay WAN group %q member %q is unavailable", egress.ID, underlayID, memberID)
			}
		}
		return members, nil
	}
	return nil, fmt.Errorf("proxy egress %q references unknown underlay WAN %q", egress.ID, underlayID)
}

func smartQoSBandwidthKbps(item map[string]any, role string) uint64 {
	keys := []string{"smart_qos_bandwidth_kbps", "bandwidth_kbps"}
	if strings.EqualFold(strings.TrimSpace(role), "lan") {
		keys = append([]string{"smart_qos_download_kbps", "download_kbps"}, keys...)
	} else if strings.EqualFold(strings.TrimSpace(role), "wan") {
		keys = append([]string{"smart_qos_upload_kbps", "upload_kbps"}, keys...)
	}
	for _, key := range keys {
		value := intField(item, key)
		if value > 0 {
			return uint64(value)
		}
	}
	return 0
}

func (server *Server) desiredInterfaceCIDRs(ctx context.Context) map[string]string {
	items, err := server.desiredItems(ctx, "interface")
	if err != nil {
		return nil
	}
	cidrs := map[string]string{}
	for _, item := range items {
		if truthy(item["deleted"]) || stringField(item, "kind") == "lan_bridge" || stringField(item, "type") == "lan_bridge" {
			continue
		}
		cidr := strings.TrimSpace(firstNonEmpty(stringField(item, "cidr"), stringField(item, "ip_cidr"), stringField(item, "address"), stringField(item, "ip")))
		if cidr == "" || !strings.Contains(cidr, "/") {
			continue
		}
		linuxInterface := server.resolveInterfaceID(ctx, firstNonEmpty(stringField(item, "interface_id"), stringField(item, "id"), stringField(item, "name")))
		if linuxInterface != "" {
			cidrs[linuxInterface] = cidr
		}
	}
	return cidrs
}

func wanDesiredIPv4Mode(item map[string]any) string {
	if ipv4, ok := item["ipv4"].(map[string]any); ok {
		if mode := strings.ToLower(strings.TrimSpace(stringField(ipv4, "mode"))); mode != "" {
			return mode
		}
	}
	return strings.ToLower(nonEmpty(stringField(item, "wan_type"), stringField(item, "type")))
}

func appendUniqueString(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func wanStaticCIDR(item map[string]any) string {
	wanType := strings.ToLower(nonEmpty(stringField(item, "wan_type"), stringField(item, "type")))
	if strings.Contains(wanType, "dhcp") || wanType == "pppoe" || wanType == "proxy" {
		return ""
	}
	if cidr := strings.TrimSpace(firstNonEmpty(stringField(item, "cidr"), stringField(item, "ip_cidr"), stringField(item, "address"))); strings.Contains(cidr, "/") {
		return cidr
	}
	if ipv4, ok := item["ipv4"].(map[string]any); ok {
		if cidr := strings.TrimSpace(nonEmpty(stringField(ipv4, "address"), stringField(ipv4, "cidr"))); strings.Contains(cidr, "/") {
			return cidr
		}
	}
	if ipv6, ok := item["ipv6"].(map[string]any); ok {
		if cidr := strings.TrimSpace(nonEmpty(stringField(ipv6, "address"), stringField(ipv6, "cidr"))); strings.Contains(cidr, "/") {
			return cidr
		}
	}
	return ""
}

func vppInterfaceName(linuxInterface string) string {
	linuxInterface = strings.TrimSpace(linuxInterface)
	if linuxInterface == "" {
		return ""
	}
	return "lyroute-" + linuxInterface
}

func vppAddressCIDR(value string) (string, bool) {
	prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
	if err != nil {
		return "", false
	}
	return prefix.String(), true
}

func vppInterfaceNameSafe(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-' || char == '.' || char == ':' {
			continue
		}
		return false
	}
	return true
}

func (server *Server) saveManagementNetwork(ctx context.Context, payload map[string]any) (map[string]any, error) {
	if confirmed, ok := payload["confirm_change"].(bool); !ok || !confirmed {
		return nil, fmt.Errorf("management network changes require confirm_change=true")
	}
	item := server.managementNetworkState(ctx, false)
	mode := normalizeManagementMode(stringField(item, "mode"))
	if value := strings.TrimSpace(stringField(payload, "mode")); value != "" {
		if value != "exclusive" && value != "shared_lan" {
			return nil, fmt.Errorf("mode must be exclusive or shared_lan")
		}
		mode = value
	}
	item["mode"] = mode
	if value := strings.TrimSpace(stringField(payload, "interface_id")); value != "" {
		current := server.managementInterfaceID(ctx)
		if !vppInterfaceNameSafe(value) || current == "" || value != current {
			return nil, fmt.Errorf("management interface is immutable and must remain %s", current)
		}
	}
	if value := strings.TrimSpace(nonEmpty(stringField(payload, "cidr"), stringField(payload, "ip_cidr"))); value != "" {
		item["cidr"] = value
	}
	if value := strings.TrimSpace(stringField(payload, "gateway")); value != "" {
		item["gateway"] = value
	}
	if value := strings.TrimSpace(stringField(payload, "dhcp_pool_start")); value != "" {
		item["dhcp_pool_start"] = value
	}
	if value := strings.TrimSpace(stringField(payload, "dhcp_pool_end")); value != "" {
		item["dhcp_pool_end"] = value
	}
	if value := strings.TrimSpace(stringField(payload, "dns")); value != "" {
		item["dns"] = value
	}
	prefix, err := netip.ParsePrefix(stringField(item, "cidr"))
	if err != nil {
		return nil, fmt.Errorf("cidr is invalid")
	}
	if gateway := stringField(item, "gateway"); gateway != "" {
		address, err := netip.ParseAddr(gateway)
		if err != nil || !prefix.Contains(address) {
			return nil, fmt.Errorf("gateway must be inside management subnet")
		}
	}
	if mode == "shared_lan" && stringField(item, "interface_id") == "" {
		return nil, fmt.Errorf("shared_lan requires a LAN interface")
	}
	if mode == "exclusive" && server.orchestratorRepository != nil {
		if topology, _, snapshotErr := server.orchestratorRepository.Snapshot(ctx); snapshotErr == nil {
			view := topology.View()
			for _, logical := range view.Interfaces {
				if logical.Role == orchestrator.RoleLAN && logical.Port == stringField(item, "interface_id") {
					return nil, fmt.Errorf("exclusive mode requires moving LAN away from the management interface first")
				}
			}
		}
	}
	for _, key := range []string{"dhcp_pool_start", "dhcp_pool_end", "dns"} {
		value := stringField(item, key)
		if value == "" {
			continue
		}
		address, err := netip.ParseAddr(value)
		if err != nil || !prefix.Contains(address) {
			return nil, fmt.Errorf("%s must be inside management subnet", key)
		}
	}
	item["new_url"] = "https://" + strings.SplitN(stringField(item, "cidr"), "/", 2)[0] + "/"
	item["rollback_guidance"] = "If access is lost, reconnect to the previous management subnet or restore the saved config backup from recovery mode."
	raw, hash, err := persistence.MarshalPayload(item)
	if err != nil {
		return nil, err
	}
	if err := server.store.SaveConfig(ctx, persistence.ConfigDocument{ResourceType: "management_network", ResourceID: "management-network", Payload: raw, PayloadHash: hash, UpdatedAt: server.now().UTC()}); err != nil {
		return nil, err
	}
	return item, nil
}

func validateDesiredPayload(resourceType string, payload map[string]any) error {
	if want, ok := map[string]string{
		"security_acl":            "acl",
		"security_ip_mac_binding": "ip_mac_binding",
		"security_threat_intel":   "threat_intel",
		"security_attack_rule":    "attack_rule",
	}[resourceType]; ok {
		if got := stringField(payload, "kind"); got != "" && got != want {
			return fmt.Errorf("%s kind must be %q", resourceType, want)
		}
		payload["kind"] = want
		if err := validateSecurityDesiredPayload(resourceType, payload); err != nil {
			return err
		}
	}
	if resourceType == "port_map" || resourceType == "route_policy" {
		if fullConeRequested(payload) {
			return fmt.Errorf("full-cone NAT requires endpoint-independent NAT44 behavior test gate pass")
		}
	}
	if resourceType == "interface" {
		copyStringAlias(payload, "description", "description", "remark", "notes")
		role := strings.TrimSpace(nonEmpty(stringField(payload, "gateway_role"), stringField(payload, "role")))
		if role != "" {
			role = strings.ToLower(role)
			if !oneOf(role, []string{"lan", "wan", "internal", "external"}) {
				return fmt.Errorf("interface gateway_role must be one of lan, wan, internal, external")
			}
			payload["gateway_role"] = role
			payload["mode_role"] = map[string]any{"gateway": role, "bridge": nil}
			payload["candidate_scopes"] = []string{role}
		}
		if stringField(payload, "work_mode") == "" {
			payload["work_mode"] = nonEmpty(stringField(payload, "active_path"), "native_auto")
		}
	}
	if resourceType == "wan_link" {
		copyStringAlias(payload, "description", "description", "remark", "notes")
		normalizeWANLinkPayload(payload)
	}
	if resourceType == "wan_group" {
		normalizeWANGroupPayload(payload)
	}
	if resourceType == "proxy_node" || resourceType == "proxy_subscription" {
		redactProxyDesiredPayload(payload)
	}
	return nil
}

func (server *Server) validateDesiredRuntimePayload(ctx context.Context, resourceType, action string, payload map[string]any) error {
	switch resourceType {
	case "interface":
		role := strings.TrimSpace(nonEmpty(stringField(payload, "gateway_role"), stringField(payload, "role")))
		interfaceID := strings.TrimSpace(nonEmpty(stringField(payload, "interface_id"), stringField(payload, "id")))
		if oneOf(role, []string{"lan", "wan"}) && interfaceID != "" && interfaceID == server.managementInterfaceID(ctx) && !server.managementNetworkShared(ctx) {
			return fmt.Errorf("management interface cannot be configured as lan or wan")
		}
		return nil
	case "nat_static":
		_, err := nat.CompileConfig([]map[string]any{payload}, nil)
		return err
	case "port_map":
		wanItems, err := server.natWANItems(ctx)
		if err != nil {
			return err
		}
		_, err = nat.CompileConfigWithWANs(nil, []map[string]any{payload}, wanItems)
		return err
	case "traffic_control":
		intent, err := flowIntentFromDesiredPayload(payload)
		if err != nil {
			return err
		}
		_, err = flow.CompileIntent(intent)
		return err
	case "dhcp_server":
		assignments, err := server.runtimeAddressAssignments(ctx)
		if err != nil {
			return err
		}
		interfaceID := strings.TrimSpace(stringField(payload, "interface_id"))
		if _, ok := server.dhcpLANControlInterface(ctx, interfaceID, assignments); !ok {
			return fmt.Errorf("DHCP service interface %s is not a configured logical LAN", interfaceID)
		}
		return nil
	case "security_acl":
		groups, err := server.desiredItems(ctx, "object_group")
		if err != nil {
			return err
		}
		_, err = trafficpolicy.CompileConfig(nil, []map[string]any{payload}, groups)
		return err
	case "proxy_egress":
		if stringField(payload, "underlay_wan_id") == "" && stringField(payload, "underlay_wan") == "" {
			return fmt.Errorf("proxy_egress requires underlay_wan_id")
		}
		if lowCopyRequested(payload) {
			return fmt.Errorf("poc_failed: low-copy proxy handoff requires Task 16 PASS")
		}
		if stringField(payload, "node_id") != "" && stringField(payload, "subscription_id") != "" {
			return fmt.Errorf("proxy_egress node_id and subscription_id are mutually exclusive")
		}
		return nil
	case "wan_group":
		return server.validateWANGroupPayload(ctx, payload)
	case "wan_link":
		return server.validateWANLinkPayload(ctx, action, payload)
	default:
		return nil
	}
}

func (server *Server) validateWANLinkPayload(ctx context.Context, action string, payload map[string]any) error {
	interfaceID := server.resolveInterfaceID(ctx, firstNonEmpty(stringField(payload, "interface_id"), stringField(payload, "system_name"), stringField(payload, "id"), stringField(payload, "name")))
	if interfaceID == "" {
		return nil
	}
	if err := validateWANLinkConfigured(payload); err != nil {
		return err
	}
	families := wanConfiguredFamilies(payload)
	if len(families) == 0 {
		return nil
	}
	items, err := server.desiredItems(ctx, "wan_link")
	if err != nil {
		return err
	}
	payloadID := stringField(payload, "id")
	for _, item := range items {
		if truthy(item["deleted"]) {
			continue
		}
		itemID := stringField(item, "id")
		if action != "create" && itemID != "" && itemID == payloadID {
			continue
		}
		itemInterface := server.resolveInterfaceID(ctx, firstNonEmpty(stringField(item, "interface_id"), stringField(item, "system_name"), stringField(item, "id"), stringField(item, "name")))
		if itemInterface != interfaceID {
			continue
		}
		itemFamilies := wanConfiguredFamilies(item)
		for family := range families {
			if itemFamilies[family] {
				return fmt.Errorf("interface %s already has %s configuration", server.displayInterfaceName(ctx, interfaceID), strings.ToUpper(family))
			}
		}
	}
	return nil
}

func validateWANLinkConfigured(item map[string]any) error {
	wanType := strings.ToLower(strings.TrimSpace(nonEmpty(stringField(item, "wan_type"), stringField(item, "type"))))
	if ipv4, ok := item["ipv4"].(map[string]any); ok {
		mode := strings.ToLower(strings.TrimSpace(stringField(ipv4, "mode")))
		if mode != "" && mode != "disabled" && mode != "none" && wanType == "" {
			wanType = mode
		}
	}
	if ipv6, ok := item["ipv6"].(map[string]any); ok {
		mode := strings.ToLower(strings.TrimSpace(stringField(ipv6, "mode")))
		if mode != "" && mode != "disabled" && mode != "none" && wanType == "" {
			wanType = mode
		}
	}
	if wanType == "" {
		return fmt.Errorf("WAN link requires a configured address mode")
	}
	switch wanType {
	case "dhcp", "dhcp4", "dhcp6":
		return nil
	case "pppoe":
		return nil
	case "static", "static4":
		cidr := firstNonEmpty(stringField(item, "cidr"), stringField(item, "ip_cidr"), stringField(item, "address"))
		gateway := firstNonEmpty(stringField(item, "gateway"), stringField(item, "next_hop"))
		hasAddressField := hasAnyKey(item, "cidr", "ip_cidr", "address")
		hasGatewayField := hasAnyKey(item, "gateway", "next_hop")
		staticMode := false
		if ipv4, ok := item["ipv4"].(map[string]any); ok {
			staticMode = strings.ToLower(strings.TrimSpace(stringField(ipv4, "mode"))) == "static"
			cidr = firstNonEmpty(cidr, stringField(ipv4, "address"), stringField(ipv4, "cidr"))
			gateway = firstNonEmpty(gateway, stringField(ipv4, "gateway"))
			hasAddressField = hasAddressField || hasAnyKey(ipv4, "address", "cidr")
			hasGatewayField = hasGatewayField || hasAnyKey(ipv4, "gateway")
		}
		if staticMode && ((hasAddressField && cidr == "") || (hasGatewayField && (gateway == "" || cidr == ""))) {
			return fmt.Errorf("static IPv4 WAN requires address and gateway")
		}
		return nil
	case "static6":
		cidr := firstNonEmpty(stringField(item, "cidr"), stringField(item, "ip_cidr"), stringField(item, "address"))
		gateway := firstNonEmpty(stringField(item, "gateway"), stringField(item, "next_hop"))
		hasAddressField := hasAnyKey(item, "cidr", "ip_cidr", "address")
		hasGatewayField := hasAnyKey(item, "gateway", "next_hop")
		staticMode := false
		if ipv6, ok := item["ipv6"].(map[string]any); ok {
			staticMode = strings.ToLower(strings.TrimSpace(stringField(ipv6, "mode"))) == "static"
			cidr = firstNonEmpty(cidr, stringField(ipv6, "address"), stringField(ipv6, "cidr"))
			gateway = firstNonEmpty(gateway, stringField(ipv6, "gateway"))
			hasAddressField = hasAddressField || hasAnyKey(ipv6, "address", "cidr")
			hasGatewayField = hasGatewayField || hasAnyKey(ipv6, "gateway")
		}
		if staticMode && ((hasAddressField && cidr == "") || (hasGatewayField && (gateway == "" || cidr == ""))) {
			return fmt.Errorf("static IPv6 WAN requires address and gateway")
		}
		return nil
	default:
		return nil
	}
}

func hasAnyKey(item map[string]any, keys ...string) bool {
	for _, key := range keys {
		if _, ok := item[key]; ok {
			return true
		}
	}
	return false
}

func wanConfiguredFamilies(item map[string]any) map[string]bool {
	families := map[string]bool{}
	if family := wanCIDRFamily(firstNonEmpty(stringField(item, "cidr"), stringField(item, "ip_cidr"), stringField(item, "address"))); family != "" {
		families[family] = true
	}
	wanType := strings.ToLower(strings.TrimSpace(nonEmpty(stringField(item, "wan_type"), stringField(item, "type"))))
	switch wanType {
	case "static", "static4", "dhcp", "dhcp4", "pppoe":
		families["ipv4"] = true
	case "static6", "dhcp6":
		families["ipv6"] = true
	}
	if ipv4, ok := item["ipv4"].(map[string]any); ok {
		mode := strings.ToLower(strings.TrimSpace(stringField(ipv4, "mode")))
		if mode != "" && mode != "disabled" && mode != "none" {
			families["ipv4"] = true
		}
		if family := wanCIDRFamily(firstNonEmpty(stringField(ipv4, "address"), stringField(ipv4, "cidr"))); family != "" {
			families[family] = true
		}
	}
	if ipv6, ok := item["ipv6"].(map[string]any); ok {
		mode := strings.ToLower(strings.TrimSpace(stringField(ipv6, "mode")))
		if mode != "" && mode != "disabled" && mode != "none" {
			families["ipv6"] = true
		}
		if family := wanCIDRFamily(firstNonEmpty(stringField(ipv6, "address"), stringField(ipv6, "cidr"))); family != "" {
			families[family] = true
		}
	}
	return families
}

func wanCIDRFamily(value string) string {
	if value == "" {
		return ""
	}
	prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
	if err == nil {
		if prefix.Addr().Is4() {
			return "ipv4"
		}
		return "ipv6"
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	if addr.Is4() {
		return "ipv4"
	}
	return "ipv6"
}

func decoratePortMapItems(items []map[string]any) []map[string]any {
	decorated := make([]map[string]any, 0, len(items))
	for _, item := range items {
		decorated = append(decorated, decoratePortMapItem(item))
	}
	return decorated
}

func decoratePortMapItem(item map[string]any) map[string]any {
	decorated := cloneObject(item)
	if _, ok := decorated["enabled"]; !ok {
		decorated["enabled"] = true
	}
	if _, ok := decorated["last_hit_at"]; !ok {
		decorated["last_hit_at"] = nil
	}
	if _, ok := decorated["session_count"]; !ok {
		decorated["session_count"] = nil
	}
	if _, ok := decorated["dataplane_observed"]; !ok {
		decorated["dataplane_observed"] = false
	}
	if _, ok := decorated["runtime_state"]; !ok {
		decorated["runtime_state"] = "desired_not_applied"
	}
	return decorated
}

func decorateTrafficControlItems(items []map[string]any) []map[string]any {
	decorated := make([]map[string]any, 0, len(items))
	for _, item := range items {
		decorated = append(decorated, decorateTrafficControlItem(item))
	}
	return decorated
}

func decorateTrafficControlItem(item map[string]any) map[string]any {
	decorated := cloneObject(item)
	if _, ok := decorated["hit_count"]; !ok {
		decorated["hit_count"] = nil
	}
	if _, ok := decorated["hit_count_state"]; !ok {
		decorated["hit_count_state"] = "unavailable"
	}
	if _, ok := decorated["hit_count_reason"]; !ok {
		decorated["hit_count_reason"] = "VPP policy hit counter readback is not configured"
	}
	return decorated
}

func normalizeWANLinkPayload(payload map[string]any) {
	wanType := strings.ToLower(nonEmpty(stringField(payload, "wan_type"), stringField(payload, "type")))
	if wanType == "" {
		wanType = "static"
	}
	payload["wan_type"] = wanType
	payload["schema_version"] = 1
	if _, ok := payload["ipv4"]; !ok {
		payload["ipv4"] = map[string]any{"mode": wanIPv4Mode(wanType)}
	}
	if _, ok := payload["ipv6"]; !ok {
		payload["ipv6"] = map[string]any{"mode": wanIPv6Mode(wanType), "ra": wanType == "dhcp", "dhcpv6": wanType == "dhcp", "prefix_delegation": wanType == "dhcp" || wanType == "pppoe"}
	}
	if _, ok := payload["passive_state"]; !ok {
		payload["passive_state"] = map[string]any{"removal_triggers": []string{"link_down", "pppoe_session_down", "dhcp_lease_expired"}, "auto_rejoin": true, "active_health_checks": false}
	}
	if wanType == "pppoe" {
		if secret := firstStringField(payload, "password", "pppoe_password"); secret != "" {
			payload["credential_ref"] = "local-secret:pppoe:" + stringField(payload, "id")
			payload["pppoe_password_redacted"] = "redacted"
			delete(payload, "password")
			delete(payload, "pppoe_password")
		}
	}
}

func normalizeWANGroupPayload(payload map[string]any) {
	payload["schema_version"] = 1
	if _, ok := payload["load_balance"]; !ok {
		payload["load_balance"] = map[string]any{"mode": "per_connection_weighted"}
	}
	if _, ok := payload["passive_state"]; !ok {
		payload["passive_state"] = map[string]any{"removal_triggers": []string{"link_down", "pppoe_session_down", "dhcp_lease_expired"}, "auto_rejoin": true, "active_health_checks": false}
	}
}

func redactProxyDesiredPayload(payload map[string]any) {
	for _, key := range []string{"secret", "token", "password", "url", "subscription_url", "uri"} {
		if value := stringField(payload, key); value != "" {
			payload[key+"_redacted"] = "redacted"
			delete(payload, key)
		}
	}
}

func normalizeProxyNodeURIPayload(payload map[string]any) error {
	raw := strings.TrimSpace(stringField(payload, "uri"))
	if raw == "" {
		address := strings.TrimSpace(stringField(payload, "address"))
		if strings.Contains(address, "://") {
			raw = address
		}
	}
	if raw == "" {
		return nil
	}
	node, err := proxy.ParseNodeURI(raw)
	if err != nil {
		return fmt.Errorf("proxy node URI: %w", err)
	}
	if stringField(payload, "id") == "" {
		payload["id"] = node.ID
	}
	if stringField(payload, "name") == "" {
		payload["name"] = node.Name
	}
	payload["protocol"] = node.Protocol
	payload["address"] = node.Address
	payload["port"] = node.Port
	payload["secret"] = node.Secret
	payload["settings"] = node.Settings
	payload["uri"] = raw
	return nil
}

func extractDesiredSecrets(resourceType string, payload map[string]any) map[string]string {
	secrets := map[string]string{}
	switch resourceType {
	case "proxy_node":
		if value := firstNonEmpty(stringField(payload, "secret"), stringField(payload, "token"), stringField(payload, "password")); value != "" {
			secrets["secret"] = value
			payload["credential_ref"] = "local-secret:proxy_node:" + stringField(payload, "id") + ":secret"
		}
	case "proxy_subscription":
		if value := firstNonEmpty(stringField(payload, "url"), stringField(payload, "subscription_url")); value != "" {
			secrets["url"] = value
			payload["credential_ref"] = "local-secret:proxy_subscription:" + stringField(payload, "id") + ":url"
		}
	case "wan_link":
		if value := firstNonEmpty(stringField(payload, "pppoe_password"), stringField(payload, "password")); value != "" {
			secrets["pppoe_password"] = value
		}
	}
	return secrets
}

func lowCopyRequested(payload map[string]any) bool {
	for _, key := range []string{"low_copy", "low_copy_enabled", "vpp_low_copy", "transparent_handoff"} {
		if truthy(payload[key]) {
			return true
		}
	}
	for _, key := range []string{"handoff", "datapath", "main_path"} {
		if strings.Contains(strings.ReplaceAll(strings.ToLower(stringField(payload, key)), "-", "_"), "low_copy") {
			return true
		}
	}
	return false
}

func wanIPv4Mode(wanType string) string {
	switch wanType {
	case "dhcp", "dhcp4":
		return "dhcp4"
	case "pppoe":
		return "pppoe"
	case "proxy":
		return "underlay"
	default:
		return "static"
	}
}

func wanIPv6Mode(wanType string) string {
	switch wanType {
	case "dhcp", "dhcp4", "dhcp6":
		return "dhcpv6_pd"
	case "pppoe":
		return "pppoe_dhcpv6_pd"
	case "proxy":
		return "underlay"
	default:
		return "static_or_slaac"
	}
}

func (server *Server) validateWANGroupPayload(ctx context.Context, payload map[string]any) error {
	members := wanGroupMemberIDs(payload)
	if len(members) < 2 {
		return fmt.Errorf("wan_group requires at least two real WAN members")
	}
	for _, member := range members {
		kind, err := server.wanMemberKind(ctx, member)
		if err != nil {
			return err
		}
		if kind != "real" {
			return fmt.Errorf("wan_group does not support proxy egress member %q", member)
		}
	}
	return nil
}

func wanGroupMemberIDs(payload map[string]any) []string {
	ids := stringSliceField(payload, "members")
	if len(ids) > 0 {
		return ids
	}
	ids = stringSliceField(payload, "wan_members")
	if len(ids) > 0 {
		return ids
	}
	for _, item := range anySliceField(payload, "member_weights", "wan_members") {
		if object, ok := item.(map[string]any); ok {
			if id := firstStringField(object, "id", "wan_id", "member_id", "egress_id"); id != "" {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

func (server *Server) wanMemberKind(ctx context.Context, id string) (string, error) {
	if item, ok, err := server.desiredItem(ctx, "wan_link", id); err != nil {
		return "", err
	} else if ok {
		if strings.EqualFold(stringField(item, "wan_type"), "proxy") || strings.EqualFold(stringField(item, "type"), "proxy") {
			return "proxy", nil
		}
		return "real", nil
	}
	if _, ok, err := server.desiredItem(ctx, "proxy_egress", id); err != nil {
		return "", err
	} else if ok {
		return "proxy", nil
	}
	return "real", nil
}

func anySliceField(item map[string]any, keys ...string) []any {
	for _, key := range keys {
		if values, ok := item[key].([]any); ok {
			return values
		}
	}
	return nil
}

func flowIntentFromDesiredPayload(payload map[string]any) (flow.Intent, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return flow.Intent{}, err
	}
	var intent flow.Intent
	if err := json.Unmarshal(raw, &intent); err != nil {
		return flow.Intent{}, err
	}
	if intent.ID == "" {
		intent.ID = stringField(payload, "id")
	}
	return intent, nil
}

func oneOf(value string, allowed []string) bool {
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}

func fullConeRequested(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			name := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(key)), "-", "_")
			if (name == "full_cone" || name == "full_cone_nat" || name == "endpoint_independent") && truthy(child) {
				return true
			}
			if (name == "nat_behavior" || name == "nat_mode") && strings.Contains(strings.ReplaceAll(strings.ToLower(fmt.Sprint(child)), "-", "_"), "full_cone") {
				return true
			}
			if fullConeRequested(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if fullConeRequested(child) {
				return true
			}
		}
	}
	return false
}

func truthy(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true") || strings.EqualFold(strings.TrimSpace(typed), "enabled")
	default:
		return false
	}
}

func cloneObject(item map[string]any) map[string]any {
	raw, _ := json.Marshal(item)
	var clone map[string]any
	_ = json.Unmarshal(raw, &clone)
	return clone
}

func readSystemSummary() (map[string]any, controlapi.CapabilityState) {
	summary := map[string]any{}
	reasons := []string{}
	if cpu, err := parseProcStat(); err == nil {
		summary["cpu_busy_percent"] = cpu
	} else {
		reasons = append(reasons, err.Error())
	}
	if memory, err := parseProcMeminfo(); err == nil {
		for key, value := range memory {
			summary[key] = value
		}
	} else {
		reasons = append(reasons, err.Error())
	}
	if load, err := parseProcLoadavg(); err == nil {
		summary["load_average"] = load
	} else {
		reasons = append(reasons, err.Error())
	}
	if len(reasons) > 0 {
		return summary, controlapi.CapabilityState{Name: "system_summary", Available: false, State: controlapi.CapabilityDegraded, Reason: strings.Join(reasons, "; ")}
	}
	return summary, controlapi.CapabilityState{Name: "system_summary", Available: true, State: controlapi.CapabilityAvailable}
}

func parseProcStat() (float64, error) {
	content, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, fmt.Errorf("/proc/stat unavailable: %w", err)
	}
	lines := strings.Split(string(content), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "cpu ") {
		return 0, fmt.Errorf("/proc/stat missing aggregate cpu row")
	}
	fields := strings.Fields(lines[0])
	if len(fields) < 8 {
		return 0, fmt.Errorf("/proc/stat aggregate cpu row is incomplete")
	}
	values := make([]uint64, 0, len(fields)-1)
	var total uint64
	for _, field := range fields[1:] {
		value, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("/proc/stat has invalid cpu counter %q", field)
		}
		values = append(values, value)
		total += value
	}
	if total == 0 {
		return 0, fmt.Errorf("/proc/stat aggregate cpu total is zero")
	}
	idle := values[3]
	if len(values) > 4 {
		idle += values[4]
	}
	return round2(float64(total-idle) * 100 / float64(total)), nil
}

func parseProcMeminfo() (map[string]any, error) {
	content, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return nil, fmt.Errorf("/proc/meminfo unavailable: %w", err)
	}
	values := map[string]uint64{}
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		values[strings.TrimSuffix(fields[0], ":")] = value * 1024
	}
	total := values["MemTotal"]
	available := values["MemAvailable"]
	if total == 0 || available == 0 {
		return nil, fmt.Errorf("/proc/meminfo missing MemTotal or MemAvailable")
	}
	used := total - available
	return map[string]any{"memory_total_bytes": total, "memory_available_bytes": available, "memory_used_bytes": used, "memory_used_percent": round2(float64(used) * 100 / float64(total))}, nil
}

func parseProcLoadavg() (map[string]float64, error) {
	content, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return nil, fmt.Errorf("/proc/loadavg unavailable: %w", err)
	}
	fields := strings.Fields(string(content))
	if len(fields) < 3 {
		return nil, fmt.Errorf("/proc/loadavg is incomplete")
	}
	keys := []string{"1m", "5m", "15m"}
	load := map[string]float64{}
	for index, key := range keys {
		value, err := strconv.ParseFloat(fields[index], 64)
		if err != nil {
			return nil, fmt.Errorf("/proc/loadavg has invalid value %q", fields[index])
		}
		load[key] = value
	}
	return load, nil
}

func clampIntQuery(raw string, fallback, minValue, maxValue int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		value = fallback
	}
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func round2(value float64) float64 {
	return float64(int(value*100+0.5)) / 100
}

func stringField(item map[string]any, key string) string {
	value, _ := item[key].(string)
	return strings.TrimSpace(value)
}

func intField(item map[string]any, key string) int {
	switch value := item[key].(type) {
	case int:
		return value
	case float64:
		return int(value)
	default:
		return 0
	}
}

func firstIntField(item map[string]any, keys ...string) int {
	for _, key := range keys {
		if value := intField(item, key); value != 0 {
			return value
		}
	}
	return 0
}

func (server *Server) degradedRuntimeCapabilities() []controlapi.CapabilityState {
	return []controlapi.CapabilityState{
		server.serviceCapability(context.Background(), serviceRuntime.VPP, "vpp_runtime_apply", "VPP apply runtime is not configured"),
		server.serviceCapability(context.Background(), serviceRuntime.SmartDNS, "smartdns_runtime_apply", "SmartDNS service runtime is not configured"),
		server.serviceCapability(context.Background(), serviceRuntime.Kea, "kea_runtime_apply", "Kea service runtime is not configured"),
		server.serviceCapability(context.Background(), serviceRuntime.Xray, "xray_runtime_apply", "xray service runtime is not configured"),
	}
}

func nonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func loadDNSSyncToken() string {
	if token := strings.TrimSpace(os.Getenv("LY_ROUTE_DNS_SYNC_TOKEN")); token != "" {
		return token
	}
	path := envOrDefault("LY_ROUTE_DNS_SYNC_TOKEN_FILE", "/etc/ly-route/dns-sync.token")
	payload, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(payload))
}

func envBoolOrDefault(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if value == "" {
		return fallback
	}
	return value == "1" || value == "true" || value == "yes" || value == "on"
}

func (server *Server) handleRuntimePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	plan, err := server.buildRuntimePlan(r.Context(), requestID(r))
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "runtime_preview_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "preview", "runtime_state": runtimeState(plan.Components), "plan": plan, "request_id": requestID(r)})
}

func (server *Server) handleRuntimeApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	session, ok := server.sessionFromRequest(r)
	if !ok && server.validDNSSyncRequest(r) {
		session = Session{Username: "dns-sync", Role: "admin"}
		ok = true
	}
	if !ok {
		server.recordAudit("anonymous", "system", "/api/v1/runtime/apply", "apply", "denied", "authentication required", r)
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	if session.Role != "admin" {
		server.recordAudit(session.Username, session.Role, "/api/v1/runtime/apply", "apply", "denied", "readonly mutation denied", r)
		writeError(w, r, http.StatusForbidden, "forbidden", "admin role required")
		return
	}
	if server.passwordChangeRequired(w, r, session, "/api/v1/runtime/apply", "apply") {
		return
	}
	server.runtimeApplyMu.Lock()
	defer server.runtimeApplyMu.Unlock()
	transactionID := "runtime-" + newRequestID()
	if err := server.refreshVPPNativeProof(r.Context()); err != nil {
		server.recordRuntimeApply(r.Context(), RuntimeApplyResult{Status: "dataplane_locked", RuntimeState: "degraded", TransactionID: transactionID, Reason: err.Error(), AppliedAt: server.now().UTC()}, session, r)
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "dataplane_locked", "runtime_state": "degraded", "reason": err.Error(), "transaction_id": transactionID, "request_id": requestID(r)})
		return
	}
	plan, err := server.buildRuntimePlan(r.Context(), transactionID)
	if err != nil {
		result := degradedRuntimeResult(transactionID, err.Error(), server.now().UTC())
		result.Status = "compile_failed"
		if recordErr := server.recordRuntimeApply(r.Context(), result, session, r); recordErr != nil {
			result.Reason += ": audit persistence: " + recordErr.Error()
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"status": result.Status, "runtime_state": result.RuntimeState, "reason": result.Reason, "transaction_id": transactionID, "request_id": requestID(r)})
		return
	}
	if plan.DataplaneState == "dataplane_locked" && len(plan.GatewayPlan.NativePath.Assignments) > 0 {
		reason := "dataplane prerequisites are not satisfied"
		if len(plan.Warnings) > 0 && strings.TrimSpace(plan.Warnings[0]) != "" {
			reason = plan.Warnings[0]
		}
		result := degradedRuntimeResult(transactionID, reason, server.now().UTC())
		result.Status = "dataplane_locked"
		result.GatewayPlan = &plan.GatewayPlan
		result.Components = plan.Components
		if recordErr := server.recordRuntimeApply(r.Context(), result, session, r); recordErr != nil {
			result.Reason += ": audit persistence: " + recordErr.Error()
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"status": result.Status, "runtime_state": result.RuntimeState, "reason": result.Reason, "transaction_id": transactionID, "components": result.Components, "dataplane_prerequisites": plan.DataplaneProof, "request_id": requestID(r)})
		return
	}
	if !server.serviceRuntimeConfigured() {
		result := degradedRuntimeResult(transactionID, "service runtime controller is not configured", server.now().UTC())
		result.Status = "unavailable"
		result.RuntimeState = "unavailable"
		server.setRuntimeEvidence(result)
		result.Components = server.applyRuntimeEvidence(plan.Components)
		if err := server.recordRuntimeApply(r.Context(), result, session, r); err != nil {
			result.Reason += ": audit persistence: " + err.Error()
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"status": result.Status, "runtime_state": result.RuntimeState, "reason": result.Reason, "transaction_id": transactionID, "components": result.Components, "request_id": requestID(r)})
		return
	}
	serviceArtifacts := runtimeServiceArtifacts(plan.RuntimeArtifacts, server.gatewayTransaction != nil)
	evidenceRequest := RuntimeEvidenceRequest{TransactionID: transactionID, Capability: "/api/v1/runtime/apply", Artifacts: serviceArtifacts}
	var appliedArtifacts []serviceRuntime.RenderedArtifact
	var capabilityFailures []apply.CapabilityFailureEvidence
	executor := apply.Executor{
		Store:   server.store,
		Now:     server.now,
		Gateway: server.gatewayTransaction,
		Apply: func(ctx context.Context, _ apply.Plan) error {
			report := server.services.ApplyCapabilitiesForTransaction(ctx, transactionID, serviceArtifacts)
			appliedArtifacts = report.AppliedArtifacts
			capabilityFailures = serviceFailureEvidence(report.Failures)
			evidenceRequest.Artifacts = appliedArtifacts
			return nil
		},
		Receipt: func(ctx context.Context, _ apply.Plan) (apply.ApplyReceipt, error) {
			return server.runtimeReceipt(ctx, evidenceRequest)
		},
		Readback: func(ctx context.Context, _ apply.Plan) (apply.Readback, error) {
			return server.runtimeReadback(ctx, evidenceRequest)
		},
		Rollback: func(ctx context.Context, applyPlan apply.Plan) error {
			serviceErr := server.services.Rollback(ctx, appliedArtifacts)
			var gatewayErr error
			if server.gatewayTransaction != nil {
				gatewayErr = server.gatewayTransaction.Rollback(ctx, applyPlan)
			}
			return errors.Join(serviceErr, gatewayErr)
		},
		CapabilityFailures: func() []apply.CapabilityFailureEvidence { return capabilityFailures },
	}
	execution, err := executor.Run(r.Context(), apply.Request{
		TransactionID:      transactionID,
		Actor:              session.Username,
		Role:               session.Role,
		Resource:           "/api/v1/runtime/apply",
		ProxyEgress:        plan.ProxyEgress,
		FlowIntent:         plan.FlowIntent,
		SnapshotID:         "snapshot-" + transactionID,
		PreviousSnapshotID: server.latestRuntimeSnapshotID(r.Context()),
		RollbackID:         "rollback-" + transactionID,
		GatewayPlan:        plan.GatewayPlan,
	})
	if err != nil {
		cause := "runtime transaction: " + err.Error()
		result := degradedRuntimeResult(transactionID, cause, server.now().UTC())
		result.Status = "apply_failed"
		result.RollbackReceipt = execution.RollbackReceipt
		server.setRuntimeEvidence(result)
		result.Components = server.applyRuntimeEvidence(plan.Components)
		server.recordAudit(session.Username, session.Role, "/api/v1/runtime/apply", "apply", "failure", cause, r)
		writeJSON(w, http.StatusAccepted, map[string]any{"status": result.Status, "runtime_state": result.RuntimeState, "reason": result.Reason, "transaction_id": transactionID, "rollback_receipt": result.RollbackReceipt, "components": result.Components, "request_id": requestID(r)})
		return
	}
	if err := server.applyLivePPPoEIPv6(r.Context()); err != nil {
		cause := "PPPoE IPv6 hot apply: " + err.Error()
		result := degradedRuntimeResult(transactionID, cause, server.now().UTC())
		result.Status = "apply_failed"
		server.setRuntimeEvidence(result)
		server.recordAudit(session.Username, session.Role, "/api/v1/runtime/apply", "apply", "failure", cause, r)
		writeJSON(w, http.StatusAccepted, map[string]any{"status": result.Status, "runtime_state": result.RuntimeState, "reason": cause, "transaction_id": transactionID, "request_id": requestID(r)})
		return
	}
	receipt, readback := execution.Receipt, execution.Readback
	server.setRuntimeEvidence(RuntimeApplyResult{Status: "committed", RuntimeState: "running", TransactionID: transactionID, Receipt: receipt, Readback: readback, GatewayPlan: &plan.GatewayPlan, GatewayEvidence: execution.GatewayResult.Evidence, AppliedAt: receipt.AppliedAt})
	components := server.runtimeStatusComponents(r.Context(), appliedArtifacts, len(plan.VppOperations) > 0)
	components = applyCapabilityFailures(components, capabilityFailures, transactionID, server.now().UTC())
	state := runtimeState(components)
	statusCode := http.StatusOK
	resultStatus := "committed"
	reason := ""
	if state != "running" {
		statusCode = http.StatusAccepted
		resultStatus = "degraded"
		reason = "runtime apply completed with degraded components"
	}
	result := RuntimeApplyResult{Status: resultStatus, RuntimeState: state, TransactionID: transactionID, Reason: reason, Components: components, Applied: artifactServiceNames(appliedArtifacts), SnapshotHash: runtimePlanHash(plan), AppliedAt: server.now().UTC(), Receipt: receipt, Readback: readback, GatewayPlan: &plan.GatewayPlan, GatewayEvidence: execution.GatewayResult.Evidence, CapabilityFailures: capabilityFailures}
	server.setRuntimeEvidence(result)
	writeJSON(w, statusCode, map[string]any{"status": result.Status, "runtime_state": result.RuntimeState, "reason": result.Reason, "transaction_id": transactionID, "components": components, "applied_services": result.Applied, "capability_failures": result.CapabilityFailures, "snapshot_hash": result.SnapshotHash, "request_id": requestID(r)})
}

func (server *Server) refreshVPPNativeProof(ctx context.Context) error {
	interfaces := server.runtimeDataInterfaces(ctx)
	if len(interfaces) == 0 {
		return nil
	}
	commandPath := envOrDefault("LY_ROUTE_VPP_PROBE_COMMAND", "/usr/lib/ly-route/runtime-check.sh")
	if _, err := os.Stat(commandPath); err != nil {
		return nil
	}
	proofPath := envOrDefault("LY_ROUTE_VPP_CAPABILITY_PROOF", "/var/lib/ly-route/vpp-native-capabilities.json")
	command := exec.CommandContext(ctx, commandPath)
	command.Env = append(os.Environ(),
		"LY_ROUTE_MANAGEMENT_INTERFACE="+server.managementInterfaceID(ctx),
		"LY_ROUTE_MANAGEMENT_SHARED="+strconv.FormatBool(server.managementNetworkShared(ctx)),
		"LY_ROUTE_VPP_DATA_INTERFACES="+strings.Join(interfaces, ","),
		"LY_ROUTE_VPP_CAPABILITY_PROOF="+proofPath,
	)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("dataplane_locked: VPP native capability proof failed: %s", strings.TrimSpace(string(output)))
	}
	return nil
}

func (server *Server) handleRuntimeStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	plan, err := server.buildRuntimePlan(r.Context(), requestID(r))
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "runtime_status_failed", err.Error())
		return
	}
	components := server.runtimeStatusComponents(r.Context(), plan.RuntimeArtifacts, len(plan.VppOperations) > 0)
	server.runtimeMu.Lock()
	last := server.lastRuntime
	server.runtimeMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"status": runtimeState(components), "components": components, "last_apply": last, "request_id": requestID(r)})
}

func (server *Server) buildRuntimePlan(ctx context.Context, requestID string) (RuntimePlan, error) {
	if server.profile.ID() == product.Orchestrator().ID() {
		return server.buildOrchestratorRuntimePlan(ctx, requestID)
	}
	proxyEgress, hasProxyEgress, err := server.runtimeProxyEgress(ctx)
	if err != nil {
		return RuntimePlan{}, err
	}
	flowIntent, hasFlowIntent, err := server.runtimeFlowIntent(ctx)
	if err != nil {
		return RuntimePlan{}, err
	}
	return server.buildRuntimePlanFromConfig(ctx, requestID, proxyEgress, hasProxyEgress, flowIntent, hasFlowIntent)
}

func (server *Server) buildRuntimePlanFromConfig(ctx context.Context, requestID string, proxyEgress proxy.Egress, hasProxyEgress bool, flowIntent flow.Intent, hasFlowIntent bool) (RuntimePlan, error) {
	addressAssignments, err := server.runtimeAddressAssignments(ctx)
	if err != nil {
		return RuntimePlan{}, err
	}
	var smartQoSAssignments []vpp.AddressAssignment
	var compiledProxy proxy.CompiledEgress
	var compiledFlow flow.CompiledIntent
	var artifacts []serviceRuntime.RenderedArtifact
	var dnsServiceNetworks []vpp.DNSServiceNetwork
	var planningWarnings []string
	proxyRuntimeWarning := ""
	var proxyEgresses []proxy.Egress
	if hasProxyEgress {
		var err error
		compiledProxy, err = proxy.CompileEgress(proxyEgress)
		if err != nil {
			return RuntimePlan{}, err
		}
		proxyEgresses = []proxy.Egress{proxyEgress}
		proxyRuntimeWarning = server.compileProxySubscription(ctx, proxyEgress, &compiledProxy)
		if proxyRuntimeWarning != "" {
			planningWarnings = append(planningWarnings, proxyRuntimeWarning)
			degradeProxyRuntime(&compiledProxy)
		}
	}
	smartQoSAssignments, err = server.runtimeSmartQoSAssignments(ctx, addressAssignments, proxyEgress, hasProxyEgress && proxyRuntimeWarning == "")
	if err != nil {
		return RuntimePlan{}, err
	}
	if hasFlowIntent {
		var err error
		runtimeFlowIntent, expandErr := server.expandFlowIntentAddressGroups(ctx, flowIntent)
		if expandErr != nil {
			return RuntimePlan{}, expandErr
		}
		compiledFlow, err = flow.CompileIntent(runtimeFlowIntent)
		if err != nil {
			return RuntimePlan{}, err
		}
	}
	compiledNAT, err := server.currentNATConfig(ctx)
	if err != nil {
		return RuntimePlan{}, err
	}
	compiledPolicy, err := server.currentTrafficPolicyConfig(ctx)
	if err != nil {
		return RuntimePlan{}, err
	}
	if hasProxyEgress && proxyRuntimeWarning == "" {
		underlayRoute, resolveErr := server.proxyUnderlayRoute(ctx, proxyEgress, compiledPolicy)
		if resolveErr != nil {
			return RuntimePlan{}, resolveErr
		}
		underlayMTU, resolveErr := server.proxyUnderlayMTU(ctx, proxyEgress)
		if resolveErr != nil {
			return RuntimePlan{}, resolveErr
		}
		if bindErr := proxy.BindServiceNetwork(&compiledProxy, underlayRoute, proxyLANRoutes(addressAssignments), underlayMTU); bindErr != nil {
			return RuntimePlan{}, bindErr
		}
		xrayArtifacts, renderErr := serviceRuntime.RenderXray(compiledProxy)
		if renderErr != nil {
			return RuntimePlan{}, renderErr
		}
		artifacts = append(artifacts, xrayArtifacts...)
	}
	securityGeneration, err := server.currentSecurityGeneration(ctx, compiledPolicy, addressAssignments)
	if err != nil {
		return RuntimePlan{}, err
	}
	dnsPolicies, dnsArtifacts, dnsNetworks, err := server.currentDNSPolicies(ctx, proxyEgresses, compiledPolicy)
	if err != nil {
		return RuntimePlan{}, err
	}
	dnsServiceNetworks = dnsNetworks
	artifacts = append(artifacts, dnsArtifacts...)
	if hasProxyEgress && proxyRuntimeWarning == "" {
		if bindErr := proxy.BindServiceNetwork(
			&compiledProxy,
			compiledProxy.ServiceNetwork.UnderlayRoute,
			proxyServiceReturnRoutes(addressAssignments, dnsServiceNetworks, proxyEgress.ID),
			compiledProxy.ServiceNetwork.MTU,
		); bindErr != nil {
			return RuntimePlan{}, bindErr
		}
		routingArtifacts, renderErr := serviceRuntime.RenderLinuxPolicyRouting(compiledProxy.LinuxPolicyRouting, dnsServiceNetworks...)
		if renderErr != nil {
			return RuntimePlan{}, renderErr
		}
		artifacts = append(artifacts, routingArtifacts...)
	} else if len(dnsServiceNetworks) > 0 {
		routingArtifacts, renderErr := serviceRuntime.RenderDNSServiceRouting(dnsServiceNetworks)
		if renderErr != nil {
			return RuntimePlan{}, renderErr
		}
		artifacts = append(artifacts, routingArtifacts...)
	}
	dnsInterception := serviceRuntime.DNSInterceptionPlan{}
	if strings.TrimSpace(compiledProxy.NftablesCapture.Table) != "" {
		nftablesArtifacts, renderErr := serviceRuntime.RenderGatewayNftablesCapture(compiledProxy.NftablesCapture, dnsInterception)
		if renderErr != nil {
			return RuntimePlan{}, renderErr
		}
		artifacts = append(artifacts, nftablesArtifacts...)
	}
	dhcpPlans, dhcpArtifacts, err := server.currentDHCPServers(ctx, addressAssignments)
	if err != nil {
		return RuntimePlan{}, err
	}
	artifacts = append(artifacts, dhcpArtifacts...)
	pppoePeers, pppoeArtifacts, err := server.currentPPPoEPeers(ctx)
	if err != nil {
		return RuntimePlan{}, err
	}
	artifacts = append(artifacts, pppoeArtifacts...)
	ipv6Artifacts, ipv6Warning := server.currentIPv6RAArtifacts(ctx)
	artifacts = append(artifacts, ipv6Artifacts...)
	if ipv6Warning != "" {
		planningWarnings = append(planningWarnings, ipv6Warning)
	}
	dataInterfaces := server.runtimeDataInterfaces(ctx)
	proofPath := strings.TrimSpace(os.Getenv("LY_ROUTE_VPP_CAPABILITY_PROOF"))
	if proofPath == "" {
		proofPath = "/var/lib/ly-route/vpp-native-capabilities.json"
	}
	nativePath := vpp.LoadNativePathRequestWithSharedManagement(proofPath, server.managementInterfaceID(ctx), dataInterfaces, server.now().UTC(), server.managementNetworkShared(ctx))
	nativePath.RequireSmartQoS = server.requireSmartQoS
	dnsInterceptionEnabled := server.profile.ID() == product.Gateway().ID() && hasLANAddressAssignment(addressAssignments)
	dataplanePrepared := false
	if composition, ok := server.gatewayTransaction.(apply.ProductionGatewayComposition); ok {
		dataplanePrepared = composition.HasDataplaneController()
	}
	runtimePolicy := runtimePolicyForAddressAssignments(compiledPolicy, addressAssignments)
	selectedPath, selectionErr := vpp.SelectNativePath(nativePath)
	smartQoSEnabled := selectionErr == nil && selectedPath.SmartQoS
	operations, err := vpp.BuildOperations(vpp.Plan{RequestID: requestID, NativePath: nativePath, DataplanePrepared: dataplanePrepared, AddressAssignments: addressAssignments, SmartQoSAssignments: smartQoSAssignments, SmartQoSEnabled: smartQoSEnabled, Proxy: compiledProxy, Flow: compiledFlow, NAT: compiledNAT, Policy: runtimePolicy, DNSInterception: dnsInterceptionEnabled, DNSServiceNetworks: dnsServiceNetworks})
	dataplaneState := "native_ready"
	var dataplaneProof []vpp.PrerequisiteResult
	warnings := append([]string(nil), planningWarnings...)
	gatewayBonds := []vpp.BondState{}
	if err != nil {
		var locked *vpp.DataplaneLockedError
		if !errors.As(err, &locked) {
			return RuntimePlan{}, err
		}
		operations = nil
		dataplaneState = locked.Code()
		dataplaneProof = locked.Prerequisites
		warnings = append(warnings, err.Error())
	}
	if err == nil && smartQoSEnabled {
		dataplaneState = "smart_qos_ready"
		dataplaneProof = selectedPath.Prerequisites
	}
	if err == nil {
		bondOperations, err := server.currentInterfaceBondOperations(ctx, requestID)
		if err != nil {
			var locked *vpp.DataplaneLockedError
			if !errors.As(err, &locked) {
				return RuntimePlan{}, err
			}
			operations = nil
			dataplaneState = locked.Code()
			dataplaneProof = append(dataplaneProof, locked.Prerequisites...)
			warnings = append(warnings, err.Error())
		} else {
			operations = append(operations, bondOperations...)
			gatewayBonds = bondStatesFromOperations(bondOperations)
		}
	}
	if dataplaneState == "dataplane_locked" {
		artifacts = nonForwardingArtifacts(artifacts)
		compiledProxy.NftablesCapture = proxy.NftablesCapturePlan{}
		compiledProxy.LinuxPolicyRouting = proxy.LinuxPolicyRoutingPlan{}
	}
	if len(operations) > 0 {
		vppArtifacts, err := serviceRuntime.RenderVPPOperations(operations)
		if err != nil {
			return RuntimePlan{}, err
		}
		artifacts = append(artifacts, vppArtifacts...)
	}
	components := server.runtimeStatusComponents(ctx, artifacts, len(operations) > 0)
	if ipv6Warning != "" {
		components = markComponentDegraded(components, string(serviceRuntime.IPv6RA), ipv6Warning)
	}
	if proxyRuntimeWarning != "" {
		components = markComponentDegraded(components, string(serviceRuntime.Xray), proxyRuntimeWarning)
	}
	gatewayInterfaces := make([]vpp.InterfaceState, 0, len(addressAssignments))
	for _, assignment := range addressAssignments {
		gatewayInterfaces = append(gatewayInterfaces, vpp.InterfaceState{Name: assignment.VPPInterface, AdminState: "up", LinkState: "up", Addresses: []string{assignment.CIDR}})
	}
	gatewayFlow := compiledFlow
	gatewayFlow.VPPGroups = nil
	for _, group := range compiledFlow.VPPGroups {
		if len(group.Objects) > 0 {
			gatewayFlow.VPPGroups = append(gatewayFlow.VPPGroups, group)
		}
	}
	gatewayPolicy := runtimePolicy
	if len(securityGeneration.ACLs) > 0 || len(securityGeneration.MACIP) > 0 || len(securityGeneration.AttackRules) > 0 {
		// The generation operation owns the complete security attachment list;
		// retaining individual ACL operations would replace that list one rule at
		// a time and make ordered policy non-deterministic.
		gatewayPolicy.SecurityACLs = nil
	}
	gatewayPlan := vpp.Plan{RequestID: requestID, NativePath: nativePath, AddressAssignments: addressAssignments, SmartQoSAssignments: smartQoSAssignments, SmartQoSEnabled: smartQoSEnabled, Interfaces: gatewayInterfaces, Bonds: gatewayBonds, Proxy: compiledProxy, Flow: gatewayFlow, NAT: compiledNAT, Policy: gatewayPolicy, Security: securityGeneration, DNSInterception: dnsInterceptionEnabled, DNSServiceNetworks: dnsServiceNetworks}
	return RuntimePlan{ProxyEgress: proxyEgress, CompiledProxy: compiledProxy, FlowIntent: flowIntent, CompiledFlow: compiledFlow, CompiledNAT: compiledNAT, CompiledPolicy: compiledPolicy, DNSPolicies: dnsPolicies, ServiceArtifacts: summarizeRuntimeArtifacts(artifacts), RuntimeArtifacts: artifacts, VppOperations: operations, NftablesCapture: compiledProxy.NftablesCapture, DNSInterception: dnsInterception, LinuxPolicyRouting: compiledProxy.LinuxPolicyRouting, DHCPServers: dhcpPlans, PPPoEPeers: summarizePPPoEPeers(pppoePeers), Components: components, Warnings: warnings, DataplaneState: dataplaneState, DataplaneProof: dataplaneProof, GatewayPlan: gatewayPlan}, nil
}

func hasLANAddressAssignment(assignments []vpp.AddressAssignment) bool {
	for _, assignment := range assignments {
		if strings.EqualFold(strings.TrimSpace(assignment.Role), "lan") && strings.TrimSpace(assignment.VPPInterface) != "" {
			return true
		}
	}
	return false
}

func runtimePolicyForAddressAssignments(policy trafficpolicy.Config, assignments []vpp.AddressAssignment) trafficpolicy.Config {
	for _, assignment := range assignments {
		if strings.EqualFold(strings.TrimSpace(assignment.Role), "wan") && strings.TrimSpace(assignment.VPPInterface) != "" {
			return policy
		}
	}
	filtered := policy
	filtered.SecurityACLs = make([]trafficpolicy.SecurityACL, 0, len(policy.SecurityACLs))
	for _, acl := range policy.SecurityACLs {
		if acl.ID != "sec-acl-default-deny-wan" {
			filtered.SecurityACLs = append(filtered.SecurityACLs, acl)
		}
	}
	return filtered
}

func nonForwardingArtifacts(artifacts []serviceRuntime.RenderedArtifact) []serviceRuntime.RenderedArtifact {
	filtered := make([]serviceRuntime.RenderedArtifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Service == serviceRuntime.VPP || artifact.Service == serviceRuntime.Nftables || artifact.Service == serviceRuntime.LinuxRouting {
			continue
		}
		filtered = append(filtered, artifact)
	}
	return filtered
}

func runtimeServiceArtifacts(artifacts []serviceRuntime.RenderedArtifact, gatewayInstalled bool) []serviceRuntime.RenderedArtifact {
	if !gatewayInstalled {
		return artifacts
	}
	// The typed gateway transaction already applied and verified the live VPP
	// graph. Persist the same operation set for reboot/crash recovery without
	// executing the legacy helper a second time.
	result := serviceRuntime.PersistOnlyForServices(artifacts, serviceRuntime.VPP)
	if len(artifactsForService(result, serviceRuntime.LinuxRouting)) == 0 {
		// Reconcile an earlier proxy or DNS routing generation. Leaving its
		// generated script in place lets systemd replay stale TAPs and FIBs when
		// SmartDNS is restarted by an otherwise unrelated configuration apply.
		result = append(result, serviceRuntime.LinuxRoutingResetArtifact())
	}
	return result
}

func (server *Server) currentNATConfig(ctx context.Context) (nat.CompiledConfig, error) {
	staticItems, err := server.desiredItems(ctx, "nat_static")
	if err != nil {
		return nat.CompiledConfig{}, err
	}
	portMapItems, err := server.desiredItems(ctx, "port_map")
	if err != nil {
		return nat.CompiledConfig{}, err
	}
	wanItems, err := server.natWANItems(ctx)
	if err != nil {
		return nat.CompiledConfig{}, err
	}
	compiled, err := nat.CompileConfigWithWANs(staticItems, portMapItems, wanItems)
	if err != nil {
		return nat.CompiledConfig{}, err
	}
	wanPaths, err := server.currentWANGroupBindings(ctx)
	if err != nil {
		return nat.CompiledConfig{}, err
	}
	for index := range compiled.StaticMappings {
		if path, ok := wanPaths[compiled.StaticMappings[index].WANInterface]; ok {
			compiled.StaticMappings[index].WANNextHop = strings.TrimSpace(path.NextHop)
		}
	}
	for index := range compiled.PortMappings {
		if path, ok := wanPaths[compiled.PortMappings[index].WANInterface]; ok {
			compiled.PortMappings[index].WANNextHop = strings.TrimSpace(path.NextHop)
		}
	}
	bindings := make(map[string]string, len(wanPaths))
	for id, path := range wanPaths {
		bindings[id] = path.VPPInterface
	}
	if err := nat.BindWANInterfaces(&compiled, bindings); err != nil {
		return nat.CompiledConfig{}, err
	}
	return compiled, nil
}

func (server *Server) natWANItems(ctx context.Context) ([]map[string]any, error) {
	items, err := server.desiredItems(ctx, "wan_link")
	if err != nil {
		return nil, err
	}
	return resolvePPPoENATWANAddresses(ctx, items, livePPPoEAddress), nil
}

type pppoeAddressLookup func(context.Context, string) (string, string, bool)

func resolvePPPoENATWANAddresses(ctx context.Context, items []map[string]any, lookup pppoeAddressLookup) []map[string]any {
	resolved := make([]map[string]any, 0, len(items))
	for _, item := range items {
		clone := make(map[string]any, len(item)+1)
		for key, value := range item {
			clone[key] = value
		}
		if strings.EqualFold(nonEmpty(stringField(clone, "wan_type"), stringField(clone, "type")), "pppoe") {
			for _, key := range []string{"external_address", "current_address", "address", "ip", "ip_cidr", "cidr"} {
				delete(clone, key)
			}
			if ipv4, _, ok := lookup(ctx, stringField(clone, "id")); ok && strings.TrimSpace(ipv4) != "" {
				clone["current_address"] = strings.TrimSpace(ipv4)
			}
		}
		resolved = append(resolved, clone)
	}
	return resolved
}

func (server *Server) natRebuildImpact(ctx context.Context, resourceType, id string, next map[string]any) map[string]any {
	if resourceType != "wan_link" || server.store == nil {
		return nil
	}
	previous, err := server.store.Config(ctx, "wan_link", id)
	if err != nil {
		return nil
	}
	var prev map[string]any
	if err := json.Unmarshal(previous.Payload, &prev); err != nil {
		return nil
	}
	previousAddress := wanAcquiredAddress(prev)
	nextAddress := wanAcquiredAddress(next)
	if previousAddress == nextAddress {
		return nil
	}
	portMaps, err := server.desiredItems(ctx, "port_map")
	if err != nil {
		return nil
	}
	affected := make([]string, 0)
	for _, item := range portMaps {
		if stringField(item, "wan_link") == id || stringField(item, "wan_interface") == id || stringField(item, "egress") == id {
			if mapID := stringField(item, "id"); mapID != "" {
				affected = append(affected, mapID)
			}
		}
	}
	if len(affected) == 0 {
		return nil
	}
	return map[string]any{"wan_link": id, "previous_address": previousAddress, "current_address": nextAddress, "affected_port_maps": affected, "runtime_state": "nat_rebuild_required"}
}

func wanAcquiredAddress(item map[string]any) string {
	for _, key := range []string{"current_address", "address", "ip", "ip_cidr", "cidr"} {
		if value := stringField(item, key); value != "" {
			return value
		}
	}
	return ""
}

func (server *Server) currentTrafficPolicyConfig(ctx context.Context) (trafficpolicy.Config, error) {
	routeItems, err := server.desiredItems(ctx, "route_policy")
	if err != nil {
		return trafficpolicy.Config{}, err
	}
	securityItems, err := server.desiredItems(ctx, "security_acl")
	if err != nil {
		return trafficpolicy.Config{}, err
	}
	objectGroupItems, err := server.desiredItems(ctx, "object_group")
	if err != nil {
		return trafficpolicy.Config{}, err
	}
	objectGroupItems, err = materializeObjectGroupItems(objectGroupItems, referencedObjectGroupIDs(objectGroupItems, append(append([]map[string]any{}, routeItems...), securityItems...)))
	if err != nil {
		return trafficpolicy.Config{}, err
	}
	domainIPSetItems, err := server.desiredItems(ctx, "domain_ip_set")
	if err != nil {
		return trafficpolicy.Config{}, err
	}
	generatedDNSRoutes, err := server.dnsFixedWANRouteItems(ctx, domainIPSetItems)
	if err != nil {
		return trafficpolicy.Config{}, err
	}
	routeItems = append(routeItems, generatedDNSRoutes...)
	compiled, err := trafficpolicy.CompileConfigWithDomainIPSet(routeItems, securityItems, objectGroupItems, domainIPSetEntries(domainIPSetItems))
	if err != nil {
		return trafficpolicy.Config{}, err
	}
	wanGroupItems, err := server.desiredItems(ctx, "wan_group")
	if err != nil {
		return trafficpolicy.Config{}, err
	}
	wanBindings, err := server.currentWANGroupBindings(ctx)
	if err != nil {
		return trafficpolicy.Config{}, err
	}
	compiled.WANGroups, err = trafficpolicy.CompileWANGroupsWithBindings(wanGroupItems, wanBindings)
	if err != nil {
		return trafficpolicy.Config{}, err
	}
	if err := trafficpolicy.BindRoutePolicyPaths(compiled.RoutePolicies, wanBindings, compiled.WANGroups); err != nil {
		return trafficpolicy.Config{}, err
	}
	return compiled, nil
}

// dnsFixedWANRouteItems turns observed SmartDNS answers into short-lived,
// highest-priority VPP route policies. The observer is identified explicitly
// by dns_rule_id; ordinary domain IP sets can never acquire DNS precedence.
func (server *Server) dnsFixedWANRouteItems(ctx context.Context, items []map[string]any) ([]map[string]any, error) {
	policy, err := server.activeDNSPolicyResource(ctx)
	if err != nil {
		return nil, err
	}
	observed := map[string][]string{}
	now := server.now().UTC()
	for _, item := range items {
		ruleID := stringField(item, "dns_rule_id")
		if ruleID == "" || !desiredEnabled(item) {
			continue
		}
		if expiresAt := stringField(item, "expires_at"); expiresAt != "" {
			deadline, parseErr := time.Parse(time.RFC3339, expiresAt)
			if parseErr != nil || !deadline.After(now) {
				continue
			}
		}
		ips := stringSliceField(item, "ips")
		if len(ips) == 0 {
			ips = stringSliceField(item, "addresses")
		}
		for _, ip := range ips {
			address, parseErr := netip.ParseAddr(strings.TrimSpace(ip))
			if parseErr != nil {
				continue
			}
			prefix := address.String() + "/128"
			if address.Is4() {
				prefix = address.String() + "/32"
			}
			if !containsString(observed[ruleID], prefix) {
				observed[ruleID] = append(observed[ruleID], prefix)
			}
		}
	}
	routes := make([]map[string]any, 0)
	for order, rule := range policy.Policy.Rules {
		if rule.Outcome.Kind != dns.OutcomeDirect || strings.TrimSpace(rule.Outcome.WANEgressID) == "" {
			continue
		}
		ips := observed[rule.ID]
		if len(ips) == 0 {
			continue
		}
		sources := append([]string(nil), rule.SourcePrefixes...)
		if len(sources) == 0 {
			sources = []string{"any"}
		}
		routes = append(routes, map[string]any{
			"id":       "dns-override-" + rule.ID,
			"enabled":  true,
			"priority": -100000 + order,
			"action":   "route",
			"egress":   rule.Outcome.WANEgressID,
			"match": map[string]any{
				"src_ip":   strings.Join(sources, ","),
				"dst_ip":   strings.Join(ips, ","),
				"protocol": "any",
				"src_port": "any",
				"dst_port": "any",
			},
		})
	}
	return routes, nil
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func (server *Server) currentWANGroupBindings(ctx context.Context) (map[string]trafficpolicy.WANPath, error) {
	bindings := map[string]trafficpolicy.WANPath{}
	wanItems, err := server.desiredItems(ctx, "wan_link")
	if err != nil {
		return nil, err
	}
	for _, item := range wanItems {
		if !desiredEnabled(item) {
			continue
		}
		id := firstStringField(item, "id", "name")
		linuxInterface := server.resolveInterfaceID(ctx, firstStringField(item, "interface_id", "linux_interface", "id", "name"))
		if id == "" || linuxInterface == "" || !vppInterfaceNameSafe(linuxInterface) {
			continue
		}
		vppInterface := vppInterfaceName(linuxInterface)
		nextHop := wanNextHop(item)
		peerID := firstStringField(item, "pppoe_peer_id", "pppoe_id", "id")
		if sessionInterface, sessionNextHop, connected := livePPPoEPath(ctx, peerID); connected {
			vppInterface = sessionInterface
			if sessionNextHop != "" {
				nextHop = sessionNextHop
			}
		}
		bindings[id] = trafficpolicy.WANPath{VPPInterface: vppInterface, NextHop: nextHop}
	}
	interfaceItems, err := server.desiredItems(ctx, "interface")
	if err != nil {
		return nil, err
	}
	for _, item := range interfaceItems {
		if !desiredEnabled(item) || !strings.EqualFold(stringField(item, "gateway_role"), "wan") {
			continue
		}
		id := firstStringField(item, "id", "name", "interface_id")
		linuxInterface := server.resolveInterfaceID(ctx, firstStringField(item, "interface_id", "linux_interface", "id", "name"))
		if id == "" || linuxInterface == "" || !vppInterfaceNameSafe(linuxInterface) {
			continue
		}
		if _, exists := bindings[id]; !exists {
			bindings[id] = trafficpolicy.WANPath{VPPInterface: vppInterfaceName(linuxInterface), NextHop: wanNextHop(item)}
		}
	}
	proxyEgress, hasProxyEgress, err := server.runtimeProxyEgress(ctx)
	if err != nil {
		return nil, err
	}
	if hasProxyEgress && strings.TrimSpace(proxyEgress.ID) != "" {
		network := proxy.ServiceNetworkForEgressID(proxyEgress.ID)
		bindings[proxyEgress.ID] = trafficpolicy.WANPath{
			VPPInterface: network.IngressVPPInterface,
			NextHop:      network.IngressHostAddress,
		}
	}
	return bindings, nil
}

func (server *Server) proxyUnderlayRoute(ctx context.Context, egress proxy.Egress, policy trafficpolicy.Config) (string, error) {
	underlayID := strings.TrimSpace(egress.UnderlayWANID)
	if underlayID == "" {
		bindings, err := server.currentWANGroupBindings(ctx)
		if err != nil {
			return "", err
		}
		candidates := make([]trafficpolicy.WANPath, 0, len(bindings))
		for id, path := range bindings {
			if id == egress.ID || strings.TrimSpace(path.VPPInterface) == "" {
				continue
			}
			candidates = append(candidates, path)
		}
		if len(candidates) == 0 {
			// This is retained for isolated unit/runtime fixtures only. The
			// persisted configuration API rejects an unbound proxy egress.
			return "local", nil
		}
		// Legacy in-memory callers predate the explicit underlay field. Keep
		// those callers deterministic without weakening the persisted API
		// validation above: the first sorted path is only a test/runtime
		// compatibility fallback, never a UI-created configuration.
		sort.Slice(candidates, func(i, j int) bool {
			left := strings.TrimSpace(candidates[i].VPPInterface) + "\x00" + strings.TrimSpace(candidates[i].NextHop)
			right := strings.TrimSpace(candidates[j].VPPInterface) + "\x00" + strings.TrimSpace(candidates[j].NextHop)
			return left < right
		})
		path := candidates[0]
		if strings.TrimSpace(path.NextHop) != "" {
			return strings.TrimSpace(path.NextHop) + " " + strings.TrimSpace(path.VPPInterface), nil
		}
		return strings.TrimSpace(path.VPPInterface), nil
	}
	for _, group := range policy.WANGroups {
		if group.ID == underlayID {
			return fmt.Sprintf("ip4-lookup-in-table %d", vpp.WANGroupTableID(group.ID)), nil
		}
	}
	bindings, err := server.currentWANGroupBindings(ctx)
	if err != nil {
		return "", err
	}
	path, exists := bindings[underlayID]
	if !exists || strings.TrimSpace(path.VPPInterface) == "" {
		return "", fmt.Errorf("proxy egress %q underlay %q has no live VPP path", egress.ID, underlayID)
	}
	if strings.TrimSpace(path.NextHop) != "" {
		return strings.TrimSpace(path.NextHop) + " " + strings.TrimSpace(path.VPPInterface), nil
	}
	return strings.TrimSpace(path.VPPInterface), nil
}

func (server *Server) proxyUnderlayMTU(ctx context.Context, egress proxy.Egress) (int, error) {
	wanIDs, err := server.proxyUnderlayWANIDs(ctx, egress)
	if err != nil {
		return 0, err
	}
	if len(wanIDs) == 0 {
		return 1500, nil
	}
	effectiveMTU := 0
	for _, wanID := range wanIDs {
		item, exists, lookupErr := server.desiredItem(ctx, "wan_link", wanID)
		if lookupErr != nil {
			return 0, lookupErr
		}
		if !exists || !desiredEnabled(item) {
			return 0, fmt.Errorf("proxy egress %q underlay WAN %q is unavailable", egress.ID, wanID)
		}
		mtu, mtuErr := wanLinkEffectiveMTU(item)
		if mtuErr != nil {
			return 0, fmt.Errorf("proxy egress %q underlay WAN %q: %w", egress.ID, wanID, mtuErr)
		}
		if effectiveMTU == 0 || mtu < effectiveMTU {
			effectiveMTU = mtu
		}
	}
	return effectiveMTU, nil
}

func (server *Server) dnsServiceUnderlayMTU(ctx context.Context, egressID string) (int, error) {
	egressID = strings.TrimSpace(egressID)
	if item, exists, err := server.desiredItem(ctx, "proxy_egress", egressID); err != nil {
		return 0, err
	} else if exists {
		if !desiredEnabled(item) {
			return 0, fmt.Errorf("DNS service egress %q is disabled", egressID)
		}
		underlayID := firstStringField(item, "underlay_wan_id", "underlay_wan")
		if underlayID == "" {
			return 0, fmt.Errorf("DNS service proxy egress %q has no underlay WAN", egressID)
		}
		return server.proxyUnderlayMTU(ctx, proxy.Egress{ID: egressID, UnderlayWANID: underlayID})
	}
	return server.proxyUnderlayMTU(ctx, proxy.Egress{ID: "dns-service-" + egressID, UnderlayWANID: egressID})
}

func wanLinkEffectiveMTU(item map[string]any) (int, error) {
	wanType := strings.ToLower(strings.TrimSpace(firstNonEmpty(stringField(item, "wan_type"), stringField(item, "type"))))
	if wanType == "" {
		if ipv4, ok := item["ipv4"].(map[string]any); ok {
			wanType = strings.ToLower(strings.TrimSpace(stringField(ipv4, "mode")))
		}
	}
	mtu := firstIntField(item, "mtu")
	if mtu == 0 {
		if wanType == "pppoe" {
			mtu = 1492
		} else {
			mtu = 1500
		}
	}
	if mru := firstIntField(item, "mru"); mru > 0 && mru < mtu {
		mtu = mru
	}
	if mtu < 576 || mtu > 9000 {
		return 0, fmt.Errorf("effective MTU %d is outside 576-9000", mtu)
	}
	return mtu, nil
}

func proxyLANRoutes(assignments []vpp.AddressAssignment) []string {
	routes := make([]string, 0)
	seen := map[string]struct{}{}
	for _, assignment := range assignments {
		if !strings.EqualFold(strings.TrimSpace(assignment.Role), "lan") {
			continue
		}
		prefix, err := netip.ParsePrefix(strings.TrimSpace(assignment.CIDR))
		if err != nil || !prefix.Addr().Is4() {
			continue
		}
		route := prefix.Masked().String()
		if _, exists := seen[route]; exists {
			continue
		}
		seen[route] = struct{}{}
		routes = append(routes, route)
	}
	sort.Strings(routes)
	return routes
}

func proxyServiceReturnRoutes(assignments []vpp.AddressAssignment, networks []vpp.DNSServiceNetwork, proxyEgressID string) []string {
	routes := proxyLANRoutes(assignments)
	seen := make(map[string]struct{}, len(routes)+len(networks))
	for _, route := range routes {
		seen[route] = struct{}{}
	}
	proxyEgressID = strings.TrimSpace(proxyEgressID)
	for _, network := range networks {
		if proxyEgressID == "" || strings.TrimSpace(network.WANEgressID) != proxyEgressID {
			continue
		}
		address, err := netip.ParseAddr(strings.TrimSpace(network.HostAddress))
		if err != nil || !address.Is4() {
			continue
		}
		route := netip.PrefixFrom(address, 32).String()
		if _, exists := seen[route]; exists {
			continue
		}
		seen[route] = struct{}{}
		routes = append(routes, route)
	}
	sort.Strings(routes)
	return routes
}

func wanNextHop(item map[string]any) string {
	for _, key := range []string{"next_hop", "gateway", "current_gateway", "peer", "assigned_gateway"} {
		if value := stringField(item, key); value != "" {
			return value
		}
	}
	for _, key := range []string{"ipv4", "addressing", "settings"} {
		if nested, ok := item[key].(map[string]any); ok {
			for _, nestedKey := range []string{"next_hop", "gateway", "current_gateway", "peer", "assigned_gateway"} {
				if value := stringField(nested, nestedKey); value != "" {
					return value
				}
			}
		}
	}
	return ""
}

func domainIPSetEntries(items []map[string]any) []trafficpolicy.DomainIPSetEntry {
	entries := make([]trafficpolicy.DomainIPSetEntry, 0, len(items))
	for _, item := range items {
		if !desiredEnabled(item) {
			continue
		}
		domain := firstStringField(item, "domain", "id", "name")
		ips := stringSliceField(item, "ips")
		if len(ips) == 0 {
			ips = stringSliceField(item, "addresses")
		}
		if domain == "" || len(ips) == 0 {
			continue
		}
		entries = append(entries, trafficpolicy.DomainIPSetEntry{Domain: domain, IPs: ips, ExpiresAt: stringField(item, "expires_at")})
	}
	return entries
}

func (server *Server) currentInterfaceBondOperations(ctx context.Context, requestID string) ([]vpp.Operation, error) {
	management := server.managementInterfaceID(ctx)
	interfaces, err := server.desiredItems(ctx, "interface")
	if err != nil {
		return nil, err
	}
	operations := make([]vpp.Operation, 0)
	for _, item := range interfaces {
		if stringField(item, "kind") != "lan_bridge" && stringField(item, "type") != "lan_bridge" {
			continue
		}
		members := stringSliceField(item, "bridge_members")
		if len(members) == 0 {
			members = stringSliceField(item, "members")
		}
		actualMembers := make([]string, 0, len(members))
		for _, member := range members {
			resolved := server.resolveInterfaceID(ctx, member)
			if resolved == management && !server.managementNetworkShared(ctx) {
				return nil, managementOperationLock(management, "vpp.l2.bridge-domain")
			}
			actualMembers = append(actualMembers, resolved)
		}
		payload := cloneObject(item)
		payload["bridge_members"] = actualMembers
		payload["members"] = actualMembers
		operations = append(operations, vpp.Operation{Name: "vpp.l2.bridge-domain", RequestID: requestID, Resource: stringField(item, "id"), Payload: payload})
	}
	items, err := server.desiredItems(ctx, "interface_bond")
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		members := stringSliceField(item, "members")
		if len(members) == 0 {
			continue
		}
		actualMembers := make([]string, 0, len(members))
		for _, member := range members {
			resolved := server.resolveInterfaceID(ctx, member)
			if resolved == management && !server.managementNetworkShared(ctx) {
				return nil, managementOperationLock(management, "vpp.interface-bond")
			}
			actualMembers = append(actualMembers, resolved)
		}
		payload := cloneObject(item)
		payload["members"] = actualMembers
		operations = append(operations, vpp.Operation{Name: "vpp.interface-bond", RequestID: requestID, Resource: stringField(item, "id"), Payload: payload})
	}
	return operations, nil
}

func bondStatesFromOperations(operations []vpp.Operation) []vpp.BondState {
	bonds := make([]vpp.BondState, 0)
	for _, operation := range operations {
		if operation.Name != "vpp.interface-bond" {
			continue
		}
		payload, ok := operation.Payload.(map[string]any)
		if !ok {
			continue
		}
		bonds = append(bonds, vpp.BondState{Name: operation.Resource, Mode: nonEmpty(stringField(payload, "mode"), "xor"), Members: stringSliceField(payload, "members")})
	}
	return bonds
}

func managementOperationLock(management, operation string) error {
	return &vpp.DataplaneLockedError{Prerequisites: []vpp.PrerequisiteResult{{Name: "management_excluded_from_operations", Interface: management, Passed: false, Reason: "operation " + operation + " references the management interface"}}}
}

func (server *Server) currentProxyEgress(ctx context.Context) (proxy.Egress, error) {
	if server.store != nil {
		documents, _, err := server.activeProxyEgressDocuments(ctx)
		if err != nil {
			return proxy.Egress{}, err
		}
		if len(documents) > 0 {
			var egress proxy.Egress
			if err := json.Unmarshal(documents[0].Payload, &egress); err != nil {
				return proxy.Egress{}, err
			}
			return egress, nil
		}
	}
	return server.proxyEgress, nil
}

func (server *Server) runtimeProxyEgress(ctx context.Context) (proxy.Egress, bool, error) {
	if server.store != nil {
		documents, storedCount, err := server.activeProxyEgressDocuments(ctx)
		if err != nil {
			return proxy.Egress{}, false, err
		}
		if len(documents) > 0 {
			var egress proxy.Egress
			if err := json.Unmarshal(documents[0].Payload, &egress); err != nil {
				return proxy.Egress{}, false, err
			}
			return egress, true, nil
		}
		if storedCount > 0 {
			return proxy.Egress{}, false, nil
		}
	}
	return server.proxyEgress, server.runtimeProxyConfigured, nil
}

func (server *Server) activeProxyEgressDocuments(ctx context.Context) ([]persistence.ConfigDocument, int, error) {
	documents, err := server.store.Configs(ctx, "proxy_egress")
	if err != nil {
		return nil, 0, err
	}
	active := make([]persistence.ConfigDocument, 0, len(documents))
	for _, document := range documents {
		var payload map[string]any
		if err := json.Unmarshal(document.Payload, &payload); err != nil {
			return nil, 0, err
		}
		if truthy(payload["deleted"]) {
			continue
		}
		active = append(active, document)
	}
	return active, len(documents), nil
}

func (server *Server) currentFlowIntent(ctx context.Context) (flow.Intent, error) {
	if server.store != nil {
		if documents, err := server.store.Configs(ctx, "traffic_control"); err != nil {
			return flow.Intent{}, err
		} else if len(documents) > 0 {
			var payload map[string]any
			if err := json.Unmarshal(documents[0].Payload, &payload); err != nil {
				return flow.Intent{}, err
			}
			intent, err := flowIntentFromDesiredPayload(payload)
			if err != nil {
				return flow.Intent{}, err
			}
			return intent, nil
		}
	}
	return server.flowIntent, nil
}

func (server *Server) runtimeFlowIntent(ctx context.Context) (flow.Intent, bool, error) {
	if server.store != nil {
		if documents, err := server.store.Configs(ctx, "traffic_control"); err != nil {
			return flow.Intent{}, false, err
		} else if len(documents) > 0 {
			var payload map[string]any
			if err := json.Unmarshal(documents[0].Payload, &payload); err != nil {
				return flow.Intent{}, false, err
			}
			intent, err := flowIntentFromDesiredPayload(payload)
			if err != nil {
				return flow.Intent{}, false, err
			}
			return intent, true, nil
		}
	}
	return server.flowIntent, server.runtimeFlowConfigured, nil
}

func (server *Server) currentDNSPolicies(ctx context.Context, proxyEgresses []proxy.Egress, trafficPolicy trafficpolicy.Config) ([]DNSPolicyResource, []serviceRuntime.RenderedArtifact, []vpp.DNSServiceNetwork, error) {
	var resources []DNSPolicyResource
	upstreams, cache, networks, err := server.currentDNSUpstreams(ctx, trafficPolicy)
	if err != nil {
		return nil, nil, nil, err
	}
	domainSets, err := server.currentDNSDomainSets(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	if server.store != nil {
		documents, err := server.store.Policies(ctx, "dns-policy")
		if err != nil {
			return nil, nil, nil, err
		}
		for _, document := range documents {
			resource, err := server.dnsPolicyResource(ctx, document.PolicyID, document.PolicyID, document.Enabled, document.Payload)
			if err != nil {
				return nil, nil, nil, err
			}
			resource.Priority = normalizedDNSPolicyPriority(document.Priority)
			resources = append(resources, resource)
		}
		if len(resources) > 0 {
			// SmartDNS evaluates one global rule set.  Render the same merged
			// first-match policy used by the decision endpoint instead of
			// concatenating independent fragments with conflicting miss rules.
			active, activeErr := server.activeDNSPolicyResource(ctx)
			if activeErr != nil {
				return nil, nil, nil, activeErr
			}
			artifacts, renderErr := serviceRuntime.RenderSmartDNSBundle([]serviceRuntime.SmartDNSPlan{{ID: "active", Render: active.Render, Upstreams: upstreams, Cache: cache, DomainSets: domainSets}})
			if renderErr != nil {
				return nil, nil, nil, renderErr
			}
			return resources, artifacts, networks, nil
		}
	}
	policy := dns.NewPolicy(dns.Reject(), []dns.Rule{})
	compiled, err := dns.CompilePolicy(policy, proxyEgresses)
	if err != nil {
		return nil, nil, nil, err
	}
	resource := DNSPolicyResource{ID: "default", Kind: "policy", Name: "Default DNS Policy", Priority: 1000, Enabled: true, Policy: policy, Render: compiled.RenderSmartDNS(), Capabilities: []controlapi.CapabilityState{{Name: "smartdns", Available: false, State: controlapi.CapabilityDegraded, Reason: "SmartDNS process manager not wired"}}}
	artifacts, err := serviceRuntime.RenderSmartDNSBundle([]serviceRuntime.SmartDNSPlan{{ID: resource.ID, Render: resource.Render, Upstreams: upstreams, Cache: cache, DomainSets: domainSets}})
	return []DNSPolicyResource{resource}, artifacts, networks, err
}

func (server *Server) currentDNSDomainSets(ctx context.Context) (map[string][]string, error) {
	var consumers []map[string]any
	if server.store != nil {
		if documents, policyErr := server.store.Policies(ctx, "dns-policy"); policyErr == nil {
			for _, document := range documents {
				var payload map[string]any
				if json.Unmarshal(document.Payload, &payload) == nil {
					consumers = append(consumers, payload)
				}
			}
		}
	}
	return server.currentDNSDomainSetsFor(ctx, consumers)
}

func (server *Server) currentDNSDomainSetsFor(ctx context.Context, consumers []map[string]any) (map[string][]string, error) {
	items, err := server.desiredItems(ctx, "object_group")
	if err != nil {
		return nil, err
	}
	items, err = materializeObjectGroupItems(items, referencedObjectGroupIDs(items, consumers))
	if err != nil {
		return nil, err
	}
	sets := make(map[string][]string)
	for _, item := range items {
		if stringField(item, "kind") != "domain" {
			continue
		}
		if enabled, exists := item["enabled"].(bool); exists && !enabled {
			continue
		}
		id := stringField(item, "id")
		if id == "" {
			continue
		}
		sets[id] = objectGroupEntries(item)
	}
	return sets, nil
}

// dnsServiceResolverServers converts hostname-based DoH endpoints into the
// IPv4 resolver set used by the VPP service-network underlay. SmartDNS keeps
// the original DoH URL; VPP only needs reachable bootstrap resolvers for the
// service-network route. Explicit IPv4 upstreams remain part of the set.
func dnsServiceResolverServers(servers, bootstrapServers []string) []string {
	resolvers := make([]string, 0, len(servers)+len(bootstrapServers))
	hasDoH := false
	for _, server := range servers {
		if serviceRuntime.IsDoHServer(server) {
			hasDoH = true
			continue
		}
		resolvers = append(resolvers, server)
	}
	if hasDoH {
		resolvers = append(resolvers, bootstrapServers...)
	}
	return resolvers
}

// dnsBootstrapProfilesFromPolicy derives the built-in bootstrap resolver
// profile from the DNS policy that selected each upstream.  The profile is a
// property of the lookup rule, not of the DoH URL: a rule using a geosite
// group must be bootstrapped through domestic resolvers, while an otherwise
// identical DoH upstream used by a non-geosite/default rule uses foreign
// resolvers.  Explicit bootstrap_profile/bootstrap_servers fields remain
// authoritative and are handled by currentDNSUpstreams.
func (server *Server) dnsBootstrapProfilesFromPolicy(ctx context.Context) (map[string]string, error) {
	profiles := make(map[string]string)
	if server.store == nil {
		return profiles, nil
	}
	documents, err := server.store.Policies(ctx, "dns-policy")
	if err != nil {
		return nil, err
	}
	groups, err := server.desiredItems(ctx, "object_group")
	if err != nil {
		return nil, err
	}
	byID := make(map[string]map[string]any, len(groups))
	for _, group := range groups {
		if id := stringField(group, "id"); id != "" {
			byID[id] = group
		}
	}
	setProfile := func(outcome dns.Outcome, domestic bool) {
		id := strings.TrimSpace(outcome.UpstreamID)
		if id == "" {
			return
		}
		if domestic {
			// Domestic wins if an upstream is intentionally shared by more than
			// one rule; using foreign bootstrap for a geosite rule is unsafe.
			profiles[id] = string(serviceRuntime.DNSBootstrapDomestic)
			return
		}
		if _, exists := profiles[id]; !exists {
			profiles[id] = string(serviceRuntime.DNSBootstrapForeign)
		}
	}
	for _, document := range documents {
		if !document.Enabled {
			continue
		}
		var policy dns.Policy
		if err := json.Unmarshal(document.Payload, &policy); err != nil {
			return nil, fmt.Errorf("decode DNS policy %q for bootstrap profile: %w", document.PolicyID, err)
		}
		setProfile(policy.Miss, false)
		for _, rule := range policy.Rules {
			domestic := false
			for _, selector := range rule.DomainSetIDs {
				selector = strings.TrimSpace(selector)
				if selector == "" {
					continue
				}
				group := byID[selector]
				if group == nil {
					// Keep compatibility with imported geosite selectors whose
					// source metadata is not materialized in the desired store.
					domestic = strings.Contains(strings.ToLower(selector), "geosite")
				} else if dnsObjectGroupIsGeoSite(group) {
					domestic = true
				}
				if domestic {
					break
				}
			}
			for _, domain := range rule.Domains {
				if strings.HasPrefix(strings.ToLower(strings.TrimSpace(domain)), "geosite:") {
					domestic = true
					break
				}
			}
			setProfile(rule.Outcome, domestic)
		}
	}
	return profiles, nil
}

func dnsObjectGroupIsGeoSite(group map[string]any) bool {
	source, _ := group["source"].(map[string]any)
	format := strings.ToLower(strings.TrimSpace(firstStringField(source, "format", "type")))
	provider := strings.ToLower(strings.TrimSpace(stringField(source, "provider")))
	name := strings.ToLower(strings.TrimSpace(firstStringField(group, "id", "name")))
	return format == "geosite" || strings.Contains(provider, "geosite") || strings.Contains(name, "geosite")
}

func (server *Server) currentDNSUpstreams(ctx context.Context, policy trafficpolicy.Config) ([]serviceRuntime.SmartDNSUpstream, serviceRuntime.SmartDNSCache, []vpp.DNSServiceNetwork, error) {
	items, err := server.desiredItems(ctx, "dns_upstream")
	if err != nil {
		return nil, serviceRuntime.SmartDNSCache{}, nil, err
	}
	bootstrapProfiles, err := server.dnsBootstrapProfilesFromPolicy(ctx)
	if err != nil {
		return nil, serviceRuntime.SmartDNSCache{}, nil, err
	}
	upstreams := make([]serviceRuntime.SmartDNSUpstream, 0, len(items))
	networks := make([]vpp.DNSServiceNetwork, 0, len(items))
	cache := serviceRuntime.SmartDNSCache{}
	for _, item := range items {
		if enabled, ok := item["enabled"].(bool); ok && !enabled {
			continue
		}
		wanID := stringField(item, "wan_egress_id")
		servers := stringSliceField(item, "servers")
		bootstrapProfile := firstStringField(item, "bootstrap_profile", "bootstrap_dns_profile")
		bootstrapServers := stringSliceField(item, "bootstrap_servers")
		if len(bootstrapServers) == 0 && bootstrapProfile != "" {
			bootstrapServers = serviceRuntime.BuiltinDNSBootstrap(bootstrapProfile).BootstrapServers
		}
		if bootstrapProfile == "" {
			bootstrapProfile = bootstrapProfiles[stringField(item, "id")]
			if bootstrapProfile == "" {
				lowerID := strings.ToLower(stringField(item, "id"))
				if strings.Contains(lowerID, "domestic") || strings.Contains(lowerID, "cn") {
					bootstrapProfile = string(serviceRuntime.DNSBootstrapDomestic)
				} else if slices.ContainsFunc(servers, serviceRuntime.IsDoHServer) {
					bootstrapProfile = string(serviceRuntime.DNSBootstrapForeign)
				}
			}
		}
		if len(bootstrapServers) == 0 && slices.ContainsFunc(servers, serviceRuntime.IsDoHServer) {
			bootstrapServers = serviceRuntime.BuiltinDNSBootstrap(bootstrapProfile).BootstrapServers
		}
		interfaceID := server.resolveInterfaceID(ctx, stringField(item, "interface_id"))
		if wanID != "" {
			serviceResolvers := dnsServiceResolverServers(servers, bootstrapServers)
			network, networkErr := vpp.DNSServiceNetworkForUpstreamID(stringField(item, "id"), wanID, serviceResolvers)
			if networkErr != nil {
				return nil, serviceRuntime.SmartDNSCache{}, nil, networkErr
			}
			underlayRoute, routeErr := server.proxyUnderlayRoute(ctx, proxy.Egress{ID: "dns-service-" + network.UpstreamID, UnderlayWANID: wanID}, policy)
			if routeErr != nil {
				return nil, serviceRuntime.SmartDNSCache{}, nil, fmt.Errorf("DNS upstream %q cannot resolve its VPP WAN route: %w", network.UpstreamID, routeErr)
			}
			underlayMTU, mtuErr := server.dnsServiceUnderlayMTU(ctx, wanID)
			if mtuErr != nil {
				return nil, serviceRuntime.SmartDNSCache{}, nil, fmt.Errorf("DNS upstream %q cannot resolve its WAN MTU: %w", network.UpstreamID, mtuErr)
			}
			if bindErr := vpp.BindDNSServiceNetwork(&network, underlayRoute, underlayMTU); bindErr != nil {
				return nil, serviceRuntime.SmartDNSCache{}, nil, bindErr
			}
			interfaceID = network.HostInterface
			networks = append(networks, network)
		}
		upstreams = append(upstreams, serviceRuntime.SmartDNSUpstream{ID: stringField(item, "id"), Servers: servers, BootstrapServers: bootstrapServers, Interface: interfaceID, WANEgressID: wanID})
		candidate := serviceRuntime.SmartDNSCache{Size: firstIntField(item, "cache_size"), TTLMin: firstIntField(item, "ttl_min_seconds", "rr_ttl_min"), TTLMax: firstIntField(item, "ttl_max_seconds", "rr_ttl_max"), Prefetch: truthy(item["prefetch"])}
		if candidate != (serviceRuntime.SmartDNSCache{}) {
			if cache != (serviceRuntime.SmartDNSCache{}) && cache != candidate {
				return nil, serviceRuntime.SmartDNSCache{}, nil, fmt.Errorf("SmartDNS cache settings must be uniform across enabled upstreams")
			}
			cache = candidate
		}
	}
	sort.Slice(upstreams, func(left, right int) bool { return upstreams[left].ID < upstreams[right].ID })
	sort.Slice(networks, func(left, right int) bool { return networks[left].UpstreamID < networks[right].UpstreamID })
	return upstreams, cache, networks, nil
}

func (server *Server) currentDHCPServers(ctx context.Context, assignments []vpp.AddressAssignment) ([]serviceRuntime.KeaDHCP4Plan, []serviceRuntime.RenderedArtifact, error) {
	items, err := server.desiredItems(ctx, "dhcp_server")
	if err != nil {
		return nil, nil, err
	}
	reservations, err := server.currentDHCPReservations(ctx)
	if err != nil {
		return nil, nil, err
	}
	var plans []serviceRuntime.KeaDHCP4Plan
	for _, item := range items {
		if enabled, ok := item["enabled"].(bool); ok && !enabled {
			continue
		}
		configuredInterface := stringField(item, "interface_id")
		controlInterface, ok := server.dhcpLANControlInterface(ctx, configuredInterface, assignments)
		if !ok {
			return nil, nil, fmt.Errorf("DHCP service interface %s is not a configured logical LAN", configuredInterface)
		}
		routers := stringSliceField(item, "routers")
		plan := serviceRuntime.KeaDHCP4Plan{ID: stringField(item, "id"), InterfaceID: controlInterface, Subnet: stringField(item, "subnet"), Pools: stringSliceField(item, "pools"), Routers: routers, NameServers: dhcpNameServers(stringSliceField(item, "name_servers"), routers), LeaseTime: firstIntField(item, "lease_time_seconds", "valid_lifetime", "valid_lifetime_seconds")}
		plan.Reservations = reservationsForDHCPPlan(plan, reservations)
		if plan.ID == "" || plan.InterfaceID == "" || plan.Subnet == "" || len(plan.Pools) == 0 {
			continue
		}
		plans = append(plans, plan)
	}
	if len(plans) == 0 {
		return plans, nil, nil
	}
	artifacts, err := serviceRuntime.RenderKeaDHCP4Config(plans)
	if err != nil {
		return nil, nil, err
	}
	return plans, artifacts, nil
}

func dhcpNameServers(configured, routers []string) []string {
	if len(configured) > 0 {
		return append([]string(nil), configured...)
	}
	return append([]string(nil), routers...)
}

func (server *Server) dhcpLANControlInterface(ctx context.Context, interfaceID string, assignments []vpp.AddressAssignment) (string, bool) {
	interfaceID = strings.TrimSpace(interfaceID)
	resolved := server.resolveInterfaceID(ctx, interfaceID)
	if resolved == "" {
		resolved = interfaceID
	}
	for _, assignment := range assignments {
		if !strings.EqualFold(strings.TrimSpace(assignment.Role), "lan") {
			continue
		}
		linuxInterface := strings.TrimSpace(assignment.LinuxInterface)
		if resolved != linuxInterface && interfaceID != strings.TrimSpace(assignment.ID) && interfaceID != strings.TrimSpace(assignment.VPPInterface) {
			continue
		}
		return vpp.LANControlPlaneHostInterface(linuxInterface), true
	}
	return "", false
}

func reservationsForDHCPPlan(plan serviceRuntime.KeaDHCP4Plan, reservations []serviceRuntime.KeaReservation) []serviceRuntime.KeaReservation {
	prefix, err := netip.ParsePrefix(strings.TrimSpace(plan.Subnet))
	if err != nil {
		return nil
	}
	filtered := make([]serviceRuntime.KeaReservation, 0, len(reservations))
	for _, reservation := range reservations {
		address, err := netip.ParseAddr(strings.TrimSpace(reservation.IPAddress))
		if err == nil && prefix.Contains(address) {
			filtered = append(filtered, reservation)
		}
	}
	return filtered
}

func (server *Server) currentDHCPReservations(ctx context.Context) ([]serviceRuntime.KeaReservation, error) {
	items, err := server.desiredItems(ctx, "dhcp_static_binding")
	if err != nil {
		return nil, err
	}
	reservations := make([]serviceRuntime.KeaReservation, 0, len(items))
	for _, item := range items {
		if enabled, ok := item["enabled"].(bool); ok && !enabled {
			continue
		}
		reservation := serviceRuntime.KeaReservation{HWAddress: firstStringField(item, "mac", "hw_address", "hwaddr"), IPAddress: firstStringField(item, "ip_address", "ip", "address"), Hostname: firstStringField(item, "hostname", "name")}
		if reservation.HWAddress == "" || reservation.IPAddress == "" {
			continue
		}
		reservations = append(reservations, reservation)
	}
	return reservations, nil
}

func (server *Server) currentPPPoEPeers(ctx context.Context) ([]serviceRuntime.PPPoEPeer, []serviceRuntime.RenderedArtifact, error) {
	items, err := server.desiredItems(ctx, "wan_link")
	if err != nil {
		return nil, nil, err
	}
	natInsideInterfaces, err := server.pppoeNATInsideInterfaces(ctx)
	if err != nil {
		return nil, nil, err
	}
	var peers []serviceRuntime.PPPoEPeer
	for _, item := range items {
		if strings.ToLower(stringField(item, "type")) != "pppoe" {
			continue
		}
		peer, err := server.pppoePeerFromDesired(ctx, item, natInsideInterfaces)
		if err != nil {
			return nil, nil, err
		}
		peers = append(peers, peer)
	}
	if len(peers) == 0 {
		return peers, nil, nil
	}
	artifacts, err := serviceRuntime.RenderPPPoEConfig(peers)
	return peers, artifacts, err
}

func (server *Server) applyLivePPPoEIPv6(ctx context.Context) error {
	interfaces, err := server.desiredItems(ctx, "interface")
	if err != nil {
		return err
	}
	for _, lan := range interfaces {
		ipv6, _ := lan["ipv6"].(map[string]any)
		wanID := stringField(ipv6, "source_wan_id")
		if strings.ToLower(stringField(ipv6, "mode")) != "delegated_prefix" || wanID == "" {
			continue
		}
		lanInterface := server.resolveInterfaceID(ctx, firstNonEmpty(stringField(lan, "interface_id"), stringField(lan, "id")))
		if !strings.HasPrefix(lanInterface, "lyroute-") {
			lanInterface = "lyroute-" + lanInterface
		}
		if err := waitForPPPoEIPv6Convergence(ctx, wanID, lanInterface); err != nil {
			return err
		}
	}
	return nil
}

func waitForPPPoEIPv6Convergence(ctx context.Context, wanID, lanInterface string) error {
	statusDir := strings.TrimSpace(os.Getenv("LY_ROUTE_PPPOE_STATUS_DIR"))
	if statusDir == "" {
		statusDir = "/run/ly-route/pppoe"
	}
	deadline := time.Now().Add(30 * time.Second)
	var lastReason string
	for time.Now().Before(deadline) {
		content, err := os.ReadFile(filepath.Join(statusDir, wanID+".json"))
		if err == nil {
			var status struct {
				State     string `json:"state"`
				Interface string `json:"interface"`
				Session   struct {
					IPv6Ready       bool   `json:"ipv6_ready"`
					DelegatedPrefix string `json:"delegated_prefix"`
				} `json:"session"`
			}
			if json.Unmarshal(content, &status) == nil && status.State == "connected" && status.Interface != "" && status.Session.IPv6Ready {
				prefix, parseErr := netip.ParsePrefix(strings.TrimSpace(status.Session.DelegatedPrefix))
				if parseErr == nil && prefix.Addr().Is6() && prefix.Bits() <= 64 {
					lanPrefix := netip.PrefixFrom(prefix.Masked().Addr(), 64)
					raw := lanPrefix.Addr().As16()
					raw[15] = 1
					lanAddress := netip.PrefixFrom(netip.AddrFrom16(raw), 64).String()
					addresses, addressErr := runLiveVPP(ctx, "show", "interface", "address", lanInterface)
					ra, raErr := runLiveVPP(ctx, "show", "ip6", "interface", lanInterface)
					addressReady := addressErr == nil && strings.Contains(addresses, lanAddress)
					raReady := raErr == nil && strings.Contains(ra, "prefix "+lanPrefix.Addr().String()+", length 64")
					if addressReady && raReady {
						return nil
					}
					lastReason = fmt.Sprintf("LAN address ready=%t, RA ready=%t", addressReady, raReady)
				} else {
					lastReason = "delegated prefix is unavailable"
				}
			} else {
				lastReason = "PPPoE IPv6CP is not ready"
			}
		} else {
			lastReason = "PPPoE status is unavailable"
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return fmt.Errorf("WAN %s IPv6 prefix delegation did not converge: %s", wanID, lastReason)
}

func runLiveVPP(ctx context.Context, args ...string) (string, error) {
	binary := strings.TrimSpace(os.Getenv("LY_ROUTE_VPPCTL"))
	if binary == "" {
		binary = "vppctl"
	}
	output, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	text := strings.TrimSpace(string(output))
	if strings.Contains(strings.ToLower(text), "unknown input") {
		return "", fmt.Errorf("%s: %s", strings.Join(args, " "), text)
	}
	return text, nil
}

func (server *Server) handlePPPoEStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	statuses, operations, err := server.pppoeStatuses(r.Context(), requestID(r))
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "pppoe_status_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": statuses, "vpp_route_handoff": operations, "request_id": requestID(r)})
}

func (server *Server) handlePPPoELifecycle(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
			return
		}
		session, ok := server.sessionFromRequest(r)
		if !ok {
			server.recordAudit("anonymous", "system", r.URL.Path, action, "denied", "authentication required", r)
			writeError(w, r, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		if session.Role != "admin" {
			server.recordAudit(session.Username, session.Role, r.URL.Path, action, "denied", "readonly mutation denied", r)
			writeError(w, r, http.StatusForbidden, "forbidden", "admin role required")
			return
		}
		if !server.serviceRuntimeConfigured() {
			reason := "PPPoE service runtime is not configured"
			server.recordAudit(session.Username, session.Role, r.URL.Path, action, "failure", reason, r)
			writeError(w, r, http.StatusServiceUnavailable, "pppoe_lifecycle_unavailable", reason)
			return
		}
		_, artifacts, err := server.currentPPPoEPeers(r.Context())
		if err != nil {
			server.recordAudit(session.Username, session.Role, r.URL.Path, action, "failure", err.Error(), r)
			writeError(w, r, http.StatusUnprocessableEntity, "pppoe_lifecycle_failed", err.Error())
			return
		}
		if len(artifacts) == 0 {
			reason := "no PPPoE WAN is configured"
			server.recordAudit(session.Username, session.Role, r.URL.Path, action, "failure", reason, r)
			writeError(w, r, http.StatusConflict, "pppoe_lifecycle_failed", reason)
			return
		}
		switch action {
		case "connect":
			err = server.services.Apply(r.Context(), artifacts)
		case "disconnect":
			err = server.services.Stop(r.Context(), serviceRuntime.PPPoE, artifacts)
		default:
			err = fmt.Errorf("unsupported PPPoE lifecycle action %q", action)
		}
		if err != nil {
			reason := serviceRuntime.Redact(err.Error())
			server.recordAudit(session.Username, session.Role, r.URL.Path, action, "failure", reason, r)
			writeError(w, r, http.StatusServiceUnavailable, "pppoe_lifecycle_failed", reason)
			return
		}
		statuses, operations, err := server.pppoeStatuses(r.Context(), requestID(r))
		if err != nil {
			server.recordAudit(session.Username, session.Role, r.URL.Path, action, "failure", err.Error(), r)
			writeError(w, r, http.StatusUnprocessableEntity, "pppoe_lifecycle_failed", err.Error())
			return
		}
		runtimeState := "connected"
		if action == "disconnect" {
			runtimeState = "disconnected"
		}
		server.recordAudit(session.Username, session.Role, r.URL.Path, action, "success", "", r)
		writeJSON(w, http.StatusOK, map[string]any{"status": runtimeState, "items": statuses, "vpp_route_handoff": operations, "runtime_state": runtimeState, "request_id": requestID(r)})
	}
}

func (server *Server) pppoeStatuses(ctx context.Context, requestID string) ([]serviceRuntime.PPPoEStatus, []vpp.Operation, error) {
	items, err := server.desiredItems(ctx, "wan_link")
	if err != nil {
		return nil, nil, err
	}
	natInsideInterfaces, err := server.pppoeNATInsideInterfaces(ctx)
	if err != nil {
		return nil, nil, err
	}
	var statuses []serviceRuntime.PPPoEStatus
	var operations []vpp.Operation
	for _, item := range items {
		if strings.ToLower(nonEmpty(stringField(item, "wan_type"), stringField(item, "type"))) != "pppoe" {
			continue
		}
		peer, err := server.pppoePeerFromDesired(ctx, item, natInsideInterfaces)
		if err != nil {
			return nil, nil, err
		}
		state := serviceRuntime.PPPoEState(nonEmpty(nonEmpty(stringField(item, "pppoe_state"), stringField(item, "state")), "disconnected"))
		assignedIPv4 := firstStringField(item, "assigned_ipv4", "current_address")
		assignedIPv6 := stringField(item, "assigned_ipv6")
		// Once a service runtime is present, live status is authoritative. Never
		// fall back to the persisted desired state after a disconnect or failure.
		if server.services != nil {
			if server.livePPPoEAvailable(ctx) {
				if ipv4, ipv6, ok := livePPPoEAddress(ctx, peer.ID); ok {
					state = serviceRuntime.PPPoEConnected
					assignedIPv4, assignedIPv6 = ipv4, ipv6
				} else {
					state = serviceRuntime.PPPoEDisconnected
					assignedIPv4, assignedIPv6 = "", ""
				}
			} else {
				state = serviceRuntime.PPPoEDisconnected
				assignedIPv4, assignedIPv6 = "", ""
			}
		}
		status, err := serviceRuntime.NewPPPoEStatus(peer, state, assignedIPv4, assignedIPv6, intField(item, "vpp_table_id"), stringField(item, "last_error"))
		if err != nil {
			return nil, nil, err
		}
		statuses = append(statuses, status)
		if status.RouteReady {
			handoff, err := serviceRuntime.PPPoEVPPRouteHandoff(status, requestID)
			if err != nil {
				return nil, nil, err
			}
			operations = append(operations, handoff...)
		}
	}
	return statuses, operations, nil
}

func (server *Server) pppoePeerFromDesired(ctx context.Context, item map[string]any, natInsideInterfaces []string) (serviceRuntime.PPPoEPeer, error) {
	id := stringField(item, "id")
	password := firstStringField(item, "password", "pppoe_password")
	if password == "" && server.store != nil {
		password, _ = server.store.Secret(ctx, "wan_link", id, "pppoe_password")
	}
	prefixGroup, ipv6LANInterfaces, err := server.pppoeIPv6LANConfig(ctx, id)
	if err != nil {
		return serviceRuntime.PPPoEPeer{}, err
	}
	return serviceRuntime.PPPoEPeer{ID: id, Interface: stringField(item, "interface_id"), Username: firstStringField(item, "username", "pppoe_username"), Password: password, MTU: intField(item, "mtu"), MRU: intField(item, "mru"), NATInsideInterfaces: append([]string(nil), natInsideInterfaces...), IPv6PrefixGroup: prefixGroup, IPv6LANInterfaces: ipv6LANInterfaces}, nil
}

func (server *Server) pppoeNATInsideInterfaces(ctx context.Context) ([]string, error) {
	assignments, err := server.runtimeAddressAssignments(ctx)
	if err != nil {
		return nil, err
	}
	interfaces := []string{}
	for _, assignment := range assignments {
		if !strings.EqualFold(strings.TrimSpace(assignment.Role), "lan") {
			continue
		}
		name := strings.TrimSpace(assignment.VPPInterface)
		if name == "" {
			name = vppInterfaceName(strings.TrimSpace(assignment.LinuxInterface))
		}
		if name != "" {
			interfaces = appendUniqueString(interfaces, name)
		}
	}
	return interfaces, nil
}

func (server *Server) livePPPoEAvailable(ctx context.Context) bool {
	if !server.serviceRuntimeConfigured() {
		return false
	}
	health, err := server.services.HealthCheck(ctx, serviceRuntime.PPPoE)
	return err == nil && len(health) > 0 && health[0].Available
}

func livePPPoEAddress(ctx context.Context, peerID string) (string, string, bool) {
	peerID = strings.TrimSpace(peerID)
	if !validPPPoEStatusID(peerID) {
		return "", "", false
	}
	select {
	case <-ctx.Done():
		return "", "", false
	default:
	}
	output, err := os.ReadFile(filepath.Join("/run/ly-route/pppoe", peerID+".json"))
	if err != nil {
		return "", "", false
	}
	return pppAddressFromStatusJSON(output)
}

func livePPPoEInterface(ctx context.Context, peerID string) (string, bool) {
	interfaceName, _, connected := livePPPoEPath(ctx, peerID)
	return interfaceName, connected
}

func livePPPoEPath(ctx context.Context, peerID string) (string, string, bool) {
	peerID = strings.TrimSpace(peerID)
	if !validPPPoEStatusID(peerID) {
		return "", "", false
	}
	select {
	case <-ctx.Done():
		return "", "", false
	default:
	}
	output, err := os.ReadFile(filepath.Join("/run/ly-route/pppoe", peerID+".json"))
	if err != nil {
		return "", "", false
	}
	return pppPathFromStatusJSON(output)
}

func pppPathFromStatusJSON(output []byte) (string, string, bool) {
	var status struct {
		State     string `json:"state"`
		Interface string `json:"interface"`
		Session   struct {
			RemoteAddress string `json:"remote_address"`
		} `json:"session"`
	}
	if err := json.Unmarshal(output, &status); err != nil || status.State != "connected" || !vppInterfaceNameSafe(strings.TrimSpace(status.Interface)) {
		return "", "", false
	}
	interfaceName := strings.TrimSpace(status.Interface)
	remoteAddress := strings.TrimSpace(status.Session.RemoteAddress)
	address, err := netip.ParseAddr(remoteAddress)
	if err != nil || !address.Is4() || address.IsUnspecified() {
		remoteAddress = ""
	} else {
		remoteAddress = address.String()
	}
	return interfaceName, remoteAddress, true
}

func validPPPoEStatusID(peerID string) bool {
	if peerID == "" || strings.ContainsAny(peerID, "\x00\n\r /\\\t") {
		return false
	}
	for _, r := range peerID {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' && r != '.' {
			return false
		}
	}
	return true
}

func pppAddressFromStatusJSON(output []byte) (string, string, bool) {
	var status struct {
		State     string `json:"state"`
		Interface string `json:"interface"`
		Session   struct {
			LocalAddress           string `json:"local_address"`
			LocalIPv6              string `json:"local_ipv6"`
			LegacyLocalIPv6Address string `json:"local_ipv6_address"`
			DelegatedPrefix        string `json:"delegated_prefix"`
		} `json:"session"`
	}
	if err := json.Unmarshal(output, &status); err != nil || status.State != "connected" || strings.TrimSpace(status.Interface) == "" {
		return "", "", false
	}
	ipv4 := strings.TrimSpace(status.Session.LocalAddress)
	ipv6 := strings.TrimSpace(firstNonEmpty(status.Session.DelegatedPrefix, status.Session.LocalIPv6, status.Session.LegacyLocalIPv6Address))
	return ipv4, ipv6, ipv4 != "" || ipv6 != ""
}

func (server *Server) runtimeStatusComponents(ctx context.Context, artifacts []serviceRuntime.RenderedArtifact, renderedVPP bool) []RuntimeComponentState {
	if components, handled := server.orchestratorTransparentRuntimeComponents(ctx); handled {
		return components
	}
	components := []RuntimeComponentState{}
	for _, service := range []struct {
		productService product.Service
		runtimeService serviceRuntime.ServiceName
	}{
		{productService: product.ServiceSmartDNS, runtimeService: serviceRuntime.SmartDNS},
		{productService: product.ServiceKea, runtimeService: serviceRuntime.Kea},
		{productService: product.ServiceXray, runtimeService: serviceRuntime.Xray},
		{productService: product.ServicePPPoE, runtimeService: serviceRuntime.PPPoE},
	} {
		if !server.profile.AllowsService(service.productService) {
			continue
		}
		component := RuntimeComponentState{Name: string(service.runtimeService)}
		if !server.serviceRuntimeConfigured() {
			component.State = "unavailable"
			component.Reason = "service runtime controller is not configured"
		} else {
			health, err := server.services.HealthCheck(ctx, service.runtimeService)
			if err != nil {
				component.State = "degraded"
				component.Reason = err.Error()
			} else if len(health) == 0 || !health[0].Available {
				component.State = "degraded"
				if len(health) > 0 {
					component.Reason = health[0].Reason
				}
			} else {
				component.State = "running"
				component.Available = true
			}
		}
		components = append(components, component)
	}
	if server.profile.AllowsService(product.ServiceVPP) {
		components = append(components, server.vppRuntimeComponent(ctx, renderedVPP))
	}
	if server.profile.AllowsService(product.ServiceNftables) {
		components = append(components, server.serviceRuntimeComponent(ctx, serviceRuntime.Nftables, "nftables_tproxy", len(artifactsForService(artifacts, serviceRuntime.Nftables)) > 0, "no nftables/TProxy rules rendered"))
	}
	if server.profile.AllowsService(product.ServiceLinuxRouting) {
		components = append(components, appliedScriptComponent("linux_routing", len(artifactsForService(artifacts, serviceRuntime.LinuxRouting)) > 0, "no Linux routing policy rendered"))
	}
	if len(artifactsForService(artifacts, serviceRuntime.IPv6RA)) > 0 {
		components = append(components, server.serviceRuntimeComponent(ctx, serviceRuntime.IPv6RA, string(serviceRuntime.IPv6RA), true, "no IPv6 RA configuration rendered"))
	}
	if server.store == nil {
		components = append(components, RuntimeComponentState{Name: "persistence", State: "unavailable", Reason: "local store is not configured"})
	} else {
		components = append(components, RuntimeComponentState{Name: "persistence", State: "running", Available: true})
	}
	components = server.applyRuntimeEvidence(components)
	components = server.attachServiceEvidence(ctx, components, artifacts)
	evidence := server.runtimeEvidence()
	return applyCapabilityFailures(components, evidence.CapabilityFailures, evidence.TransactionID, evidence.AppliedAt)
}

func (server *Server) serviceRuntimeConfigured() bool {
	return server.services != nil && server.services.Controller != nil
}

func renderedComponent(name string, rendered bool, reason string) RuntimeComponentState {
	if rendered {
		return RuntimeComponentState{Name: name, State: "rendered", Reason: reason}
	}
	return RuntimeComponentState{Name: name, State: "unavailable", Reason: reason}
}

func (server *Server) serviceRuntimeComponent(ctx context.Context, service serviceRuntime.ServiceName, name string, rendered bool, unavailableReason string) RuntimeComponentState {
	if !rendered {
		return RuntimeComponentState{Name: name, State: "unavailable", Reason: unavailableReason}
	}
	if !server.serviceRuntimeConfigured() {
		return RuntimeComponentState{Name: name, State: "unavailable", Reason: "service runtime controller is not configured"}
	}
	health, err := server.services.HealthCheck(ctx, service)
	if err != nil {
		return RuntimeComponentState{Name: name, State: "degraded", Reason: err.Error()}
	}
	if len(health) == 0 || !health[0].Available {
		reason := string(service) + " runtime is not active"
		if len(health) > 0 && strings.TrimSpace(health[0].Reason) != "" {
			reason = health[0].Reason
		}
		return RuntimeComponentState{Name: name, State: "degraded", Reason: reason}
	}
	return RuntimeComponentState{Name: name, State: "running", Available: true}
}

func appliedScriptComponent(name string, rendered bool, unavailableReason string) RuntimeComponentState {
	if !rendered {
		return RuntimeComponentState{Name: name, State: "unavailable", Reason: unavailableReason}
	}
	return RuntimeComponentState{Name: name, State: "running", Available: true}
}

type vppApplyReceipt struct {
	TransactionID string    `json:"transaction_id"`
	Status        string    `json:"status"`
	DryRun        bool      `json:"dry_run"`
	AppliedAt     time.Time `json:"applied_at"`
	ReadbackAt    time.Time `json:"readback_at"`
	Operations    []struct {
		Name     string `json:"name"`
		Resource string `json:"resource"`
		Results  []struct {
			Command string `json:"command"`
			Status  string `json:"status"`
			Driver  string `json:"driver"`
			Hook    string `json:"hook"`
			Mode    string `json:"mode"`
		} `json:"results"`
	} `json:"operations"`
}

func (server *Server) vppRuntimeComponent(ctx context.Context, rendered bool) RuntimeComponentState {
	evidence := server.runtimeEvidence()
	if evidence.Status == RuntimeStatusCommitted && len(evidence.GatewayEvidence) > 0 && server.serviceRuntimeConfigured() {
		health, err := server.services.HealthCheck(ctx, serviceRuntime.VPP)
		if err == nil && len(health) > 0 && health[0].Available {
			return RuntimeComponentState{Name: "vpp", State: "running", Available: true, Reason: "typed gateway apply and live VPP readback verified"}
		}
	}
	if !rendered {
		if evidence.Status == RuntimeStatusCommitted && evidence.GatewayPlan != nil && len(evidence.GatewayPlan.NativePath.Assignments) > 0 && server.serviceRuntimeConfigured() {
			health, err := server.services.HealthCheck(ctx, serviceRuntime.VPP)
			if err == nil && len(health) > 0 && health[0].Available {
				return RuntimeComponentState{Name: "vpp", State: "running", Available: true, Reason: "native dataplane is active; no VPP configuration delta"}
			}
		}
		return RuntimeComponentState{Name: "vpp", State: "unavailable", Reason: "no VPP operations rendered"}
	}
	if !server.serviceRuntimeConfigured() {
		return RuntimeComponentState{Name: "vpp", State: "unavailable", Reason: "service runtime controller is not configured"}
	}
	health, err := server.services.HealthCheck(ctx, serviceRuntime.VPP)
	if err != nil {
		return RuntimeComponentState{Name: "vpp", State: "degraded", Reason: err.Error()}
	}
	if len(health) == 0 || !health[0].Available {
		reason := "vpp.service is not active"
		if len(health) > 0 && strings.TrimSpace(health[0].Reason) != "" {
			reason = health[0].Reason
		}
		return RuntimeComponentState{Name: "vpp", State: "degraded", Reason: reason}
	}
	receipt, err := server.readVPPApplyReceipt()
	if err != nil {
		return RuntimeComponentState{Name: "vpp", State: "rendered", Reason: err.Error()}
	}
	if receipt.DryRun {
		return RuntimeComponentState{Name: "vpp", State: "degraded", Reason: "VPP apply receipt is dry-run only"}
	}
	if receipt.Status != "applied" && receipt.Status != "ready" {
		return RuntimeComponentState{Name: "vpp", State: "degraded", Reason: "VPP apply receipt status is " + receipt.Status}
	}
	age := server.now().UTC().Sub(receipt.ReadbackAt)
	if receipt.TransactionID == "" || receipt.TransactionID != server.runtimeEvidence().TransactionID || receipt.AppliedAt.IsZero() || receipt.ReadbackAt.IsZero() || age < 0 || age > apply.ReadbackFreshnessWindow {
		return RuntimeComponentState{Name: "vpp", State: "degraded", Reason: "VPP apply receipt/readback timestamp is incomplete or stale"}
	}
	if len(receipt.Operations) == 0 {
		return RuntimeComponentState{Name: "vpp", State: "degraded", Reason: "VPP apply receipt has no operations"}
	}
	for _, operation := range receipt.Operations {
		if strings.TrimSpace(operation.Name) == "" || len(operation.Results) == 0 {
			return RuntimeComponentState{Name: "vpp", State: "degraded", Reason: "VPP apply receipt has incomplete operation results"}
		}
		for _, result := range operation.Results {
			if result.Status != "applied" && result.Status != "ignored-failure" {
				return RuntimeComponentState{Name: "vpp", State: "degraded", Reason: "VPP apply command did not apply: " + result.Command}
			}
		}
	}
	return RuntimeComponentState{Name: "vpp", State: "running", Available: true, Reason: fmt.Sprintf("VPP apply receipt applied %d operations", len(receipt.Operations))}
}

func (server *Server) readVPPApplyReceipt() (vppApplyReceipt, error) {
	path := strings.TrimSpace(server.vppReceiptPath)
	if path == "" {
		return vppApplyReceipt{}, fmt.Errorf("VPP apply receipt path is not configured")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return vppApplyReceipt{}, fmt.Errorf("VPP apply receipt is not available: %v", err)
	}
	var receipt vppApplyReceipt
	if err := json.Unmarshal(content, &receipt); err != nil {
		return vppApplyReceipt{}, fmt.Errorf("VPP apply receipt is invalid: %v", err)
	}
	return receipt, nil
}

func (server *Server) vppReceiptPolicyHits(runtimeStateValue string) ([]map[string]any, []controlapi.CapabilityState) {
	receipt, err := server.readVPPApplyReceipt()
	if err != nil {
		return []map[string]any{}, []controlapi.CapabilityState{{Name: "vpp_policy_readback", Available: false, State: controlapi.CapabilityDegraded, Reason: err.Error()}}
	}
	items := make([]map[string]any, 0, len(receipt.Operations))
	for _, operation := range receipt.Operations {
		if !isPolicyReadbackOperation(operation.Name) {
			continue
		}
		applied, failed := 0, 0
		commands := make([]map[string]any, 0, len(operation.Results))
		for _, result := range operation.Results {
			if result.Status == "applied" || result.Status == "ignored-failure" {
				applied++
			} else {
				failed++
			}
			commands = append(commands, map[string]any{"command": result.Command, "status": result.Status})
		}
		state := "applied"
		if failed > 0 || receipt.Status != "applied" || receipt.DryRun {
			state = "degraded"
		}
		items = append(items, map[string]any{"id": operation.Resource, "operation": operation.Name, "runtime_state": runtimeStateValue, "readback_state": state, "applied_commands": applied, "failed_commands": failed, "hits": 0, "hit_source": "vpp_apply_receipt", "commands": commands})
	}
	if len(items) == 0 {
		return items, []controlapi.CapabilityState{{Name: "vpp_policy_readback", Available: false, State: controlapi.CapabilityDegraded, Reason: "VPP apply receipt has no policy/NAT readback operations"}}
	}
	return items, []controlapi.CapabilityState{{Name: "vpp_policy_readback", Available: true, State: controlapi.CapabilityAvailable}}
}

func normalizePolicyHits(items []map[string]any, runtimeStateValue, source string) []map[string]any {
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		clone := cloneObject(item)
		clone["runtime_state"] = runtimeStateValue
		if stringField(clone, "hit_source") == "" {
			clone["hit_source"] = source
		}
		if _, ok := clone["hits"]; !ok {
			clone["hits"] = 0
		}
		result = append(result, clone)
	}
	return result
}

func isPolicyReadbackOperation(name string) bool {
	switch name {
	case "vpp.route-policy", "vpp.security-acl", "vpp.nat44-ed.static-mapping", "vpp.pbr.next-hop-group":
		return true
	default:
		return false
	}
}

func runtimeState(components []RuntimeComponentState) string {
	state := "running"
	for _, component := range components {
		if component.State == "not_configured" {
			continue
		}
		if component.State == "unavailable" {
			return "degraded"
		}
		if component.State != "running" {
			state = "degraded"
		}
	}
	return state
}

func (server *Server) recordRuntimeApply(ctx context.Context, result RuntimeApplyResult, session Session, r *http.Request) error {
	server.setRuntimeEvidence(result)
	status := "success"
	if result.Status != "committed" {
		status = "failure"
	}
	server.recordAudit(session.Username, session.Role, "/api/v1/runtime/apply", "apply", status, result.Reason, r)
	if server.store == nil {
		return nil
	}
	payload, hash, err := persistence.MarshalPayload(result)
	if err != nil {
		return fmt.Errorf("marshal runtime apply result: %w", err)
	}
	event := persistence.AuditEvent{ID: "runtime-apply-" + result.TransactionID, Timestamp: result.AppliedAt, Actor: session.Username, Role: session.Role, Resource: "/api/v1/runtime/apply", Action: "apply", AfterHash: hash, Status: status, Error: auditSafeText(result.Reason), TransactionID: result.TransactionID}
	if result.Status != RuntimeStatusCommitted || !completeRuntimeEvidence(result, server.now().UTC()) {
		if err := server.store.SaveAuditEvents(ctx, []persistence.AuditEvent{event}); err != nil {
			return fmt.Errorf("save runtime apply audit: %w", err)
		}
		return nil
	}
	if err := server.store.SaveApply(ctx, persistence.ApplyRecord{Snapshot: persistence.RuntimeSnapshot{ID: "snapshot-" + result.TransactionID, SourceTransactionID: result.TransactionID, Payload: payload, PayloadHash: hash, CreatedAt: result.AppliedAt}, AuditEvents: []persistence.AuditEvent{event}}); err != nil {
		return fmt.Errorf("save committed runtime generation: %w", err)
	}
	return nil
}

func runtimePlanHash(plan RuntimePlan) string {
	_, hash, err := persistence.MarshalPayload(plan)
	if err != nil {
		return ""
	}
	return hash
}

func artifactServiceNames(artifacts []serviceRuntime.RenderedArtifact) []string {
	seen := map[string]struct{}{}
	var names []string
	for _, artifact := range artifacts {
		name := string(artifact.Service)
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names
}

func summarizeRuntimeArtifacts(artifacts []serviceRuntime.RenderedArtifact) []RuntimeArtifactSummary {
	summaries := make([]RuntimeArtifactSummary, 0, len(artifacts))
	for _, artifact := range artifacts {
		summaries = append(summaries, RuntimeArtifactSummary{Service: artifact.Service, Path: artifact.Path, ContentHash: artifact.ContentHash, ReloadMode: artifact.ReloadMode})
	}
	return summaries
}

func summarizePPPoEPeers(peers []serviceRuntime.PPPoEPeer) []RuntimePPPoEPeerSummary {
	summaries := make([]RuntimePPPoEPeerSummary, 0, len(peers))
	for _, peer := range peers {
		summaries = append(summaries, RuntimePPPoEPeerSummary{ID: peer.ID, Interface: peer.Interface, Username: peer.Username, MTU: peer.MTU, MRU: peer.MRU})
	}
	return summaries
}

func stringSliceField(item map[string]any, key string) []string {
	values, ok := item[key].([]any)
	if !ok {
		if strings, ok := item[key].([]string); ok {
			return append([]string(nil), strings...)
		}
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			result = append(result, text)
		}
	}
	return result
}

func firstStringField(item map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringField(item, key); value != "" {
			return value
		}
	}
	return ""
}

func (server *Server) handleConfigApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	session, ok := server.sessionFromRequest(r)
	if !ok {
		server.recordAudit("anonymous", "system", "/api/v1/config/apply", "apply", "denied", "authentication required", r)
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	if session.Role != "admin" {
		server.recordAudit(session.Username, session.Role, "/api/v1/config/apply", "apply", "denied", "readonly mutation denied", r)
		writeError(w, r, http.StatusForbidden, "forbidden", "admin role required")
		return
	}
	if server.passwordChangeRequired(w, r, session, "/api/v1/config/apply", "apply") {
		return
	}
	if server.store == nil {
		writeError(w, r, http.StatusServiceUnavailable, "store_unavailable", "local store is not configured")
		return
	}
	server.runtimeApplyMu.Lock()
	defer server.runtimeApplyMu.Unlock()
	var req ConfigApplyRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := decodeStrictJSON(r, &req); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
			return
		}
	}
	transactionID := "txn-" + newRequestID()
	runtimePlan, proxyEgress, flowIntent, err := server.configApplyPlan(r.Context(), transactionID, req.ProxyEgress, req.FlowIntent)
	if err != nil {
		server.recordAudit(session.Username, session.Role, "/api/v1/config/apply", "apply", "failure", err.Error(), r)
		writeError(w, r, http.StatusUnprocessableEntity, "config_apply_plan_failed", err.Error())
		return
	}
	if runtimePlan.DataplaneState == "dataplane_locked" {
		reason := "dataplane_locked: forwarding prerequisites failed"
		if len(runtimePlan.Warnings) > 0 {
			reason = runtimePlan.Warnings[0]
		}
		server.recordAudit(session.Username, session.Role, "/api/v1/config/apply", "apply", "failure", reason, r)
		writeJSON(w, http.StatusLocked, map[string]any{
			"status":                  "dataplane_locked",
			"runtime_state":           "degraded",
			"reason":                  reason,
			"dataplane_prerequisites": runtimePlan.DataplaneProof,
			"transaction_id":          transactionID,
			"request_id":              requestID(r),
		})
		return
	}
	serviceArtifacts := runtimeServiceArtifacts(runtimePlan.RuntimeArtifacts, server.gatewayTransaction != nil)
	evidenceRequest := RuntimeEvidenceRequest{TransactionID: transactionID, Capability: "/api/v1/config/apply", Artifacts: serviceArtifacts}
	appliedArtifacts := serviceArtifacts
	var capabilityFailures []apply.CapabilityFailureEvidence
	executor := apply.Executor{
		Store: server.store,
		Now:   server.now,
		Apply: func(ctx context.Context, _ apply.Plan) error {
			if server.services == nil {
				return errors.New("live runtime apply runner is not configured")
			}
			report := server.services.ApplyCapabilitiesForTransaction(ctx, transactionID, serviceArtifacts)
			appliedArtifacts = report.AppliedArtifacts
			capabilityFailures = serviceFailureEvidence(report.Failures)
			evidenceRequest.Artifacts = appliedArtifacts
			return nil
		},
		Gateway: server.gatewayTransaction,
		Receipt: func(ctx context.Context, _ apply.Plan) (apply.ApplyReceipt, error) {
			return server.runtimeReceipt(ctx, evidenceRequest)
		},
		HealthCheck: func(context.Context, apply.Plan) error {
			if server.services == nil {
				return errors.New("live runtime health checker is not configured")
			}
			if len(artifactsForService(appliedArtifacts, serviceRuntime.Xray)) == 0 {
				return nil
			}
			health, err := server.services.HealthCheck(r.Context(), serviceRuntime.Xray)
			if err != nil {
				return err
			}
			for _, item := range health {
				if !item.Available {
					return fmt.Errorf("%s health degraded: %s", item.Service, item.Reason)
				}
			}
			return nil
		},
		Readback: func(ctx context.Context, _ apply.Plan) (apply.Readback, error) {
			return server.runtimeReadback(ctx, evidenceRequest)
		},
		Rollback: func(ctx context.Context, plan apply.Plan) error {
			if !plan.Previous.Available {
				return server.rollbackRuntimePlan(ctx, RuntimePlan{}, plan)
			}
			previous, err := server.buildRuntimePlanFromConfig(ctx, transactionID+"-rollback", plan.Previous.ProxyEgress, true, plan.Previous.FlowIntent, true)
			if err != nil {
				return fmt.Errorf("build prior runtime snapshot: %w", err)
			}
			return server.rollbackRuntimePlan(ctx, previous, plan)
		},
		CapabilityFailures: func() []apply.CapabilityFailureEvidence { return capabilityFailures },
	}
	result, err := executor.Run(r.Context(), apply.Request{
		TransactionID:      transactionID,
		Actor:              session.Username,
		Role:               session.Role,
		Resource:           "/api/v1/config/apply",
		ProxyEgress:        proxyEgress,
		FlowIntent:         flowIntent,
		SnapshotID:         "snapshot-" + transactionID,
		PreviousSnapshotID: server.latestRuntimeSnapshotID(r.Context()),
		RollbackID:         "rollback-" + transactionID,
		GatewayPlan:        runtimePlan.GatewayPlan,
	})
	if err != nil {
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "apply_failed", "runtime_state": "degraded", "reason": err.Error(), "transaction_id": transactionID, "rollback": rollbackResponse(result.Rollback), "rollback_receipt": result.RollbackReceipt, "events": applyEventResponses(result.Events), "request_id": requestID(r)})
		return
	}
	runtimeResult := RuntimeApplyResult{Status: RuntimeStatusCommitted, RuntimeState: RuntimeStateRunning, TransactionID: transactionID, Receipt: result.Receipt, Readback: result.Readback, GatewayPlan: &runtimePlan.GatewayPlan, GatewayEvidence: result.GatewayResult.Evidence, AppliedAt: result.Receipt.AppliedAt, CapabilityFailures: capabilityFailures}
	statusCode := http.StatusOK
	if len(capabilityFailures) > 0 {
		runtimeResult.Status = apply.ReceiptDegraded
		runtimeResult.RuntimeState = "degraded"
		runtimeResult.Reason = "runtime apply completed with degraded capabilities"
		statusCode = http.StatusAccepted
	}
	server.setRuntimeEvidence(runtimeResult)
	writeJSON(w, statusCode, map[string]any{"status": runtimeResult.Status, "runtime_state": runtimeResult.RuntimeState, "reason": runtimeResult.Reason, "transaction_id": transactionID, "apply_receipt": result.Receipt, "readback": result.Readback, "capability_failures": capabilityFailures, "gateway_resources": result.GatewayResult.Order, "events": applyEventResponses(result.Events), "request_id": requestID(r)})
}

func (server *Server) configApplyPlan(ctx context.Context, requestID string, overrideProxy *proxy.Egress, overrideFlow *flow.Intent) (RuntimePlan, proxy.Egress, flow.Intent, error) {
	proxyEgress, err := server.currentProxyEgress(ctx)
	if err != nil {
		return RuntimePlan{}, proxy.Egress{}, flow.Intent{}, err
	}
	if overrideProxy != nil {
		proxyEgress = *overrideProxy
	}
	flowIntent, err := server.currentFlowIntent(ctx)
	if err != nil {
		return RuntimePlan{}, proxy.Egress{}, flow.Intent{}, err
	}
	if overrideFlow != nil {
		flowIntent = *overrideFlow
	}
	plan, err := server.buildRuntimePlanFromConfig(ctx, requestID, proxyEgress, true, flowIntent, true)
	if err != nil {
		return RuntimePlan{}, proxy.Egress{}, flow.Intent{}, err
	}
	return plan, proxyEgress, flowIntent, nil
}

func rollbackResponse(rollback persistence.RollbackMetadata) map[string]any {
	if rollback.ID == "" {
		return nil
	}
	return map[string]any{
		"id":                 rollback.ID,
		"target_snapshot_id": rollback.TargetSnapshotID,
		"reason":             rollback.Reason,
		"status":             rollback.Status,
		"requested_at":       rollback.RequestedAt,
		"completed_at":       rollback.CompletedAt,
		"error":              rollback.Error,
	}
}

func applyEventResponses(events []persistence.AuditEvent) []map[string]any {
	items := make([]map[string]any, 0, len(events))
	for _, event := range events {
		items = append(items, map[string]any{
			"id":             event.ID,
			"timestamp":      event.Timestamp,
			"actor":          event.Actor,
			"role":           event.Role,
			"resource":       event.Resource,
			"action":         event.Action,
			"before_hash":    event.BeforeHash,
			"after_hash":     event.AfterHash,
			"status":         event.Status,
			"error":          auditSafeText(event.Error),
			"transaction_id": event.TransactionID,
		})
	}
	return items
}

func (server *Server) applyRuntimePlan(ctx context.Context, runtimePlan RuntimePlan, artifacts []serviceRuntime.RenderedArtifact) error {
	return server.services.Apply(ctx, artifacts)
}

func (server *Server) rollbackRuntimePlan(ctx context.Context, runtimePlan RuntimePlan, executorPlan apply.Plan) error {
	rollbackErrors := make([]error, 0, 2)
	if server.gatewayTransaction != nil {
		if err := server.gatewayTransaction.Rollback(ctx, executorPlan); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback Gateway: %w", err))
		}
	}
	if server.services == nil || !executorPlan.Previous.Available {
		return errors.Join(rollbackErrors...)
	}
	if err := server.services.Rollback(ctx, runtimeServiceArtifacts(runtimePlan.RuntimeArtifacts, server.gatewayTransaction != nil)); err != nil {
		rollbackErrors = append(rollbackErrors, fmt.Errorf("rollback services: %w", err))
	}
	return errors.Join(rollbackErrors...)
}

func (server *Server) handleConfigExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	if _, ok := server.sessionFromRequest(r); !ok {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	state, err := server.exportDesiredConfig(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "config_export_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"package_manifest": configManifest(state), "payload": state, "request_id": requestID(r)})
}

func (server *Server) handleConfigImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	session, ok := server.sessionFromRequest(r)
	if !ok {
		server.recordAudit("anonymous", "system", "/api/v1/config/import", "import", "denied", "authentication required", r)
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	if session.Role != "admin" {
		server.recordAudit(session.Username, session.Role, "/api/v1/config/import", "import", "denied", "readonly mutation denied", r)
		writeError(w, r, http.StatusForbidden, "forbidden", "admin role required")
		return
	}
	if server.passwordChangeRequired(w, r, session, "/api/v1/config/import", "import") {
		return
	}
	var req ConfigImportRequest
	if err := decodeStrictJSON(r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
		return
	}
	if err := server.preflightConfigPackage(req.Payload, req.PackageManifest); err != nil {
		code := "invalid_import_package"
		if errors.Is(err, ErrConfigProductMismatch) {
			code = "product_mismatch"
		}
		writeError(w, r, http.StatusUnprocessableEntity, code, err.Error())
		return
	}
	if configPayloadContainsSecret(req.Payload) {
		writeError(w, r, http.StatusUnprocessableEntity, "secret_material_rejected", "import package contains forbidden secret material")
		return
	}
	documents, err := configDocumentsFromImport(req.Payload, server.now().UTC(), server.profile)
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "invalid_import_package", err.Error())
		return
	}
	packageHash := hashConfigPayload(req.Payload)
	diffHash := hashObject(map[string]any{"current": "desired_config", "incoming": packageHash})
	if !req.Confirm {
		writeJSON(w, http.StatusOK, map[string]any{"status": "dry_run", "safe_to_apply": true, "rollback_snapshot_required": true, "package_hash": packageHash, "diff_hash": diffHash, "confirmation_token": newRequestID(), "confirmation_actor": session.Username, "confirmation_expires_at": server.now().UTC().Add(15 * time.Minute), "request_id": requestID(r)})
		return
	}
	if err := server.validateImportConfirmation(req, session.Username, packageHash, diffHash); err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "confirmation_required", err.Error())
		return
	}
	snapshot, err := server.createConfigSnapshot(r.Context(), "pre-import-"+newRequestID(), "config-import")
	if err != nil {
		server.recordAudit(session.Username, session.Role, "/api/v1/config/import", "import", "failure", err.Error(), r)
		writeError(w, r, http.StatusInternalServerError, "snapshot_create_failed", "pre-import snapshot could not be created")
		return
	}
	if err := server.store.SaveConfigs(r.Context(), documents); err != nil {
		server.recordAudit(session.Username, session.Role, "/api/v1/config/import", "import", "failure", err.Error(), r)
		writeError(w, r, http.StatusInternalServerError, "config_import_failed", "desired config could not be imported")
		return
	}
	server.recordAudit(session.Username, session.Role, "/api/v1/config/import", "import", "success", "", r)
	writeJSON(w, http.StatusOK, map[string]any{"status": "persisted", "safe_to_apply": true, "runtime_state": "desired_not_applied", "package_hash": packageHash, "diff_hash": diffHash, "rollback_snapshot_id": snapshot.ID, "imported_resources": len(documents), "request_id": requestID(r)})
}

func (server *Server) handleFirmwareUpdateStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	if _, ok := server.sessionFromRequest(r); !ok {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	server.firmwareMu.Lock()
	status := server.firmwareStatus
	server.firmwareMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"status": status, "request_id": requestID(r)})
}

func (server *Server) handleFirmwareUpdateStage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	session, ok := server.sessionFromRequest(r)
	if !ok {
		server.recordAudit("anonymous", "system", "/api/v1/firmware/update/stage", "firmware_stage", "denied", "authentication required", r)
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	if session.Role != "admin" {
		server.recordAudit(session.Username, session.Role, "/api/v1/firmware/update/stage", "firmware_stage", "denied", "readonly mutation denied", r)
		writeError(w, r, http.StatusForbidden, "forbidden", "admin role required")
		return
	}
	if server.passwordChangeRequired(w, r, session, "/api/v1/firmware/update/stage", "firmware_stage") {
		return
	}
	if err := r.ParseMultipartForm(1024 << 20); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_multipart", "firmware package upload is required")
		return
	}
	firmwareFile, _, err := r.FormFile("firmware")
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "missing_firmware", "firmware file is required")
		return
	}
	defer firmwareFile.Close()
	stageDir := server.firmwareStageDir
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		writeError(w, r, http.StatusInternalServerError, "firmware_stage_failed", err.Error())
		return
	}
	firmwarePath := filepath.Join(stageDir, "upgrade-"+newRequestID()+".tar.zst")
	out, err := os.OpenFile(firmwarePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "firmware_stage_failed", err.Error())
		return
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(out, hash), firmwareFile)
	closeErr := out.Close()
	if copyErr != nil {
		writeError(w, r, http.StatusInternalServerError, "firmware_stage_failed", copyErr.Error())
		return
	}
	if closeErr != nil {
		writeError(w, r, http.StatusInternalServerError, "firmware_stage_failed", closeErr.Error())
		return
	}
	imageHash := hex.EncodeToString(hash.Sum(nil))
	if err := validateUpgradePackage(firmwarePath, server.profile.ID()); err != nil {
		if removeErr := os.Remove(firmwarePath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			writeError(w, r, http.StatusInternalServerError, "firmware_stage_cleanup_failed", removeErr.Error())
			return
		}
		writeError(w, r, http.StatusUnprocessableEntity, "invalid_upgrade_package", err.Error())
		return
	}
	configBackupPath, configBackupHash, configBackupSize, err := server.stageFirmwareConfigBackup(r.Context(), stageDir)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "config_backup_failed", err.Error())
		return
	}
	server.firmwareMu.Lock()
	server.firmwareStatus = FirmwareUpdateStatus{
		Staged:           true,
		ImagePath:        firmwarePath,
		ImageHash:        imageHash,
		ImageSize:        written,
		ConfigBackupPath: configBackupPath,
		ConfigBackupHash: configBackupHash,
		ConfigBackupSize: configBackupSize,
		StagedAt:         server.now(),
		InstallCommand:   "校验升级包后复制 ly-route-control 到 /usr/lib/ly-route，并更新控制台前端资源后重启服务",
	}
	status := server.firmwareStatus
	server.firmwareMu.Unlock()
	server.recordAudit(session.Username, session.Role, "/api/v1/firmware/update/stage", "firmware_stage", "success", firmwarePath, r)
	writeJSON(w, http.StatusOK, map[string]any{"status": status, "request_id": requestID(r)})
}

func (server *Server) handleFirmwareUpdateInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	session, ok := server.sessionFromRequest(r)
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	if session.Role != "admin" {
		writeError(w, r, http.StatusForbidden, "forbidden", "admin role required")
		return
	}
	if server.passwordChangeRequired(w, r, session, "/api/v1/firmware/update/install", "firmware_install") {
		return
	}
	var req firmwareInstallRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := decodeStrictJSON(r, &req); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
			return
		}
	}
	if !req.ConfirmInstall {
		writeError(w, r, http.StatusUnprocessableEntity, "install_not_confirmed", "firmware install requires confirm_install=true")
		return
	}
	targetDir := strings.TrimSpace(req.TargetDir)
	if targetDir == "" {
		targetDir = "/usr/lib/ly-route"
	}
	if !safeFirmwareTargetDir(targetDir) {
		writeError(w, r, http.StatusUnprocessableEntity, "invalid_target_dir", "target_dir must be /usr/lib/ly-route or /opt/ly-route-upgrades/<name>")
		return
	}
	server.firmwareMu.Lock()
	status := server.firmwareStatus
	if !status.Staged || status.ImagePath == "" || status.ImageHash == "" {
		server.firmwareMu.Unlock()
		writeError(w, r, http.StatusConflict, "firmware_not_staged", "firmware must be uploaded and verified before install")
		return
	}
	if status.Installing {
		server.firmwareMu.Unlock()
		writeError(w, r, http.StatusConflict, "firmware_installing", "firmware install is already running")
		return
	}
	server.firmwareStatus.Installing = true
	server.firmwareStatus.InstallStatus = "installing"
	status = server.firmwareStatus
	server.firmwareMu.Unlock()
	invocation := firmwareUpgradeInstallInvocation(status.ImagePath, status.ImageHash, targetDir, req.Reboot)
	if err := server.firmwareInstallStart(invocation); err != nil {
		server.firmwareMu.Lock()
		server.firmwareStatus.Installing = false
		server.firmwareStatus.InstallStatus = "failed"
		server.firmwareStatus.Reason = err.Error()
		server.firmwareMu.Unlock()
		writeError(w, r, http.StatusInternalServerError, "firmware_install_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"status": status, "target_dir": targetDir, "reboot": req.Reboot, "request_id": requestID(r)})
}

func safeFirmwareTargetDir(path string) bool {
	if !strings.HasPrefix(path, "/") || strings.Contains(path, " ") || strings.Contains(path, "..") {
		return false
	}
	if path == "/usr/lib/ly-route" {
		return true
	}
	name := strings.TrimPrefix(path, "/opt/ly-route-upgrades/")
	if name == path || name == "" || strings.Contains(name, "/") {
		return false
	}
	for _, char := range name {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '.' || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func (server *Server) stageFirmwareConfigBackup(ctx context.Context, stageDir string) (string, string, int64, error) {
	state, err := server.exportDesiredConfig(ctx)
	if err != nil {
		return "", "", 0, err
	}
	packagePayload := map[string]any{"package_manifest": configManifest(state), "payload": state, "created_at": server.now().UTC()}
	body, err := json.MarshalIndent(packagePayload, "", "  ")
	if err != nil {
		return "", "", 0, err
	}
	body = append(body, '\n')
	backupPath := filepath.Join(stageDir, "ly-route-config-backup.json")
	if err := os.WriteFile(backupPath, body, 0o600); err != nil {
		return "", "", 0, err
	}
	hash := sha256.Sum256(body)
	return backupPath, hex.EncodeToString(hash[:]), int64(len(body)), nil
}

func firmwareExpectedSHA256(body []byte) string {
	fields := strings.Fields(string(body))
	if len(fields) == 0 {
		return ""
	}
	expected := strings.ToLower(strings.TrimSpace(fields[0]))
	if len(expected) != sha256.Size*2 {
		return ""
	}
	if _, err := hex.DecodeString(expected); err != nil {
		return ""
	}
	return expected
}

func (server *Server) handleConfigSnapshots(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if _, ok := server.sessionFromRequest(r); !ok {
			writeError(w, r, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		if server.store == nil {
			writeError(w, r, http.StatusServiceUnavailable, "store_unavailable", "local store is not configured")
			return
		}
		snapshots, err := server.store.RuntimeSnapshots(r.Context())
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "snapshot_list_failed", "snapshots could not be listed")
			return
		}
		items := make([]map[string]any, 0, len(snapshots))
		for _, snapshot := range snapshots {
			items = append(items, snapshotResponse(snapshot, server.profile.ID()))
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "capabilities": []controlapi.CapabilityState{{Name: "config_snapshots", Available: true, State: controlapi.CapabilityAvailable}}, "request_id": requestID(r)})
	case http.MethodPost:
		server.handleConfigSnapshotMutation(w, r, "create_snapshot")
	default:
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
	}
}

func (server *Server) handleConfigSnapshotRestore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	server.handleConfigSnapshotMutation(w, r, "restore_snapshot")
}

func (server *Server) handleConfigFactoryReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	server.handleConfigSnapshotMutation(w, r, "factory_reset")
}

func (server *Server) handleConfigSnapshotMutation(w http.ResponseWriter, r *http.Request, action string) {
	session, ok := server.sessionFromRequest(r)
	if !ok {
		server.recordAudit("anonymous", "system", r.URL.Path, action, "denied", "authentication required", r)
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	if session.Role != "admin" {
		server.recordAudit(session.Username, session.Role, r.URL.Path, action, "denied", "readonly mutation denied", r)
		writeError(w, r, http.StatusForbidden, "forbidden", "admin role required")
		return
	}
	if server.passwordChangeRequired(w, r, session, r.URL.Path, action) {
		return
	}
	if server.store == nil {
		writeError(w, r, http.StatusServiceUnavailable, "store_unavailable", "local store is not configured")
		return
	}
	switch action {
	case "create_snapshot":
		var req ConfigSnapshotRequest
		if r.Body != nil && r.ContentLength != 0 {
			if err := decodeStrictJSON(r, &req); err != nil {
				writeError(w, r, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
				return
			}
		}
		snapshot, err := server.createConfigSnapshot(r.Context(), snapshotID(req.Name), "manual")
		if err != nil {
			server.recordAudit(session.Username, session.Role, r.URL.Path, action, "failure", err.Error(), r)
			writeError(w, r, http.StatusInternalServerError, "snapshot_create_failed", "snapshot could not be created")
			return
		}
		server.recordAudit(session.Username, session.Role, r.URL.Path, action, "success", "", r)
		writeJSON(w, http.StatusOK, map[string]any{"status": "created", "action": action, "snapshot": snapshotResponse(snapshot, server.profile.ID()), "runtime_state": "desired_not_applied", "request_id": requestID(r)})
	case "restore_snapshot":
		id, _ := splitPathRemainder("/api/v1/config/snapshots/", r.URL.Path)
		if id == "" {
			writeError(w, r, http.StatusNotFound, "snapshot_not_found", "snapshot id is required")
			return
		}
		snapshot, err := server.store.RuntimeSnapshot(r.Context(), id)
		if err != nil {
			writeError(w, r, http.StatusNotFound, "snapshot_not_found", "snapshot does not exist")
			return
		}
		if err := server.restoreConfigSnapshot(r.Context(), snapshot); err != nil {
			if errors.Is(err, ErrConfigProductMismatch) || errors.Is(err, persistence.ErrPayloadIntegrity) || errors.Is(err, ErrInvalidConfigPackage) {
				code := "invalid_snapshot"
				if errors.Is(err, ErrConfigProductMismatch) {
					code = "product_mismatch"
				}
				writeError(w, r, http.StatusUnprocessableEntity, code, err.Error())
				return
			}
			writeError(w, r, http.StatusInternalServerError, "snapshot_restore_failed", "snapshot could not be restored")
			return
		}
		server.recordAudit(session.Username, session.Role, r.URL.Path, action, "success", "", r)
		writeJSON(w, http.StatusOK, map[string]any{"status": "restored", "action": action, "snapshot": snapshotResponse(snapshot, server.profile.ID()), "runtime_state": "desired_not_applied", "request_id": requestID(r)})
	case "factory_reset":
		snapshot, err := server.createConfigSnapshot(r.Context(), "pre-factory-reset-"+newRequestID(), "factory-reset")
		if err != nil {
			server.recordAudit(session.Username, session.Role, r.URL.Path, action, "failure", err.Error(), r)
			writeError(w, r, http.StatusInternalServerError, "snapshot_create_failed", "pre-reset snapshot could not be created")
			return
		}
		defaultSnapshot, err := defaultConfigSnapshot(server.profile)
		if err != nil {
			server.recordAudit(session.Username, session.Role, r.URL.Path, action, "failure", err.Error(), r)
			writeError(w, r, http.StatusInternalServerError, "factory_reset_failed", "factory reset package could not be created")
			return
		}
		if err := server.restoreConfigSnapshot(r.Context(), defaultSnapshot); err != nil {
			server.recordAudit(session.Username, session.Role, r.URL.Path, action, "failure", err.Error(), r)
			writeError(w, r, http.StatusInternalServerError, "factory_reset_failed", "factory reset could not be applied")
			return
		}
		server.recordAudit(session.Username, session.Role, r.URL.Path, action, "success", "", r)
		writeJSON(w, http.StatusOK, map[string]any{"status": "reset", "action": action, "preserve_admin_account": true, "preserve_management_port": true, "snapshot": snapshotResponse(snapshot, server.profile.ID()), "runtime_state": "desired_not_applied", "request_id": requestID(r)})
	}
}

func (server *Server) exportDesiredConfig(ctx context.Context) (ConfigPackagePayload, error) {
	resources := map[string][]json.RawMessage{}
	for resourceType := range desiredResourceDefs {
		if !resourceAllowed(server.profile, resourceType) {
			continue
		}
		items, err := server.desiredItems(ctx, resourceType)
		if err != nil {
			return ConfigPackagePayload{}, err
		}
		resources[resourceType], err = encodeResourceItems(items)
		if err != nil {
			return ConfigPackagePayload{}, err
		}
	}
	if resourceAllowed(server.profile, "proxy_egress") {
		proxyItems, err := server.proxyEgressResources(ctx)
		if err != nil {
			return ConfigPackagePayload{}, err
		}
		resources["proxy_egress"], err = encodeResourceItems(proxyItems)
		if err != nil {
			return ConfigPackagePayload{}, err
		}
	}
	return ConfigPackagePayload{SchemaVersion: ConfigPackageSchemaVersion, ContentType: configContentType, Product: server.profile.ID(), DeviceMode: server.profile.ID().String(), Resources: resources, Excluded: []string{"rendered_config", "runtime_state", "audit_logs", "secrets", "snapshots"}}, nil
}

func (server *Server) createConfigSnapshot(ctx context.Context, id, source string) (persistence.RuntimeSnapshot, error) {
	payload, err := server.exportDesiredConfig(ctx)
	if err != nil {
		return persistence.RuntimeSnapshot{}, err
	}
	raw, hash, err := persistence.MarshalPayload(payload)
	if err != nil {
		return persistence.RuntimeSnapshot{}, err
	}
	snapshot := persistence.RuntimeSnapshot{ID: id, SourceTransactionID: source, Payload: raw, PayloadHash: hash, CreatedAt: server.now().UTC()}
	if err := server.store.SaveRuntimeSnapshot(ctx, snapshot); err != nil {
		return persistence.RuntimeSnapshot{}, err
	}
	return snapshot, nil
}

func (server *Server) restoreConfigSnapshot(ctx context.Context, snapshot persistence.RuntimeSnapshot) error {
	if err := persistence.VerifyPayload(snapshot.Payload, snapshot.PayloadHash); err != nil {
		return err
	}
	var payload ConfigPackagePayload
	if err := json.Unmarshal(snapshot.Payload, &payload); err != nil {
		return err
	}
	if err := payload.ValidateFor(server.profile); err != nil {
		return err
	}
	documents, err := configDocumentsFromImport(payload, server.now().UTC(), server.profile)
	if err != nil {
		return err
	}
	return server.store.ReplaceConfigsForTypes(ctx, managedConfigResourceTypes(server.profile), documents)
}

func managedConfigResourceTypes(profile product.Profile) []string {
	types := make([]string, 0, len(desiredResourceDefs)+1)
	for resourceType := range desiredResourceDefs {
		if resourceAllowed(profile, resourceType) {
			types = append(types, resourceType)
		}
	}
	if resourceAllowed(profile, "proxy_egress") {
		types = append(types, "proxy_egress")
	}
	return types
}

func defaultConfigSnapshot(profile product.Profile) (persistence.RuntimeSnapshot, error) {
	payload := ConfigPackagePayload{SchemaVersion: ConfigPackageSchemaVersion, ContentType: configContentType, Product: profile.ID(), DeviceMode: profile.ID().String(), Resources: map[string][]json.RawMessage{}, Excluded: []string{"rendered_config", "runtime_state", "audit_logs", "secrets", "snapshots"}}
	raw, hash, err := persistence.MarshalPayload(payload)
	if err != nil {
		return persistence.RuntimeSnapshot{}, fmt.Errorf("marshal default config snapshot: %w", err)
	}
	return persistence.RuntimeSnapshot{Payload: raw, PayloadHash: hash}, nil
}

func configDocumentsFromImport(payload ConfigPackagePayload, updatedAt time.Time, profile product.Profile) ([]persistence.ConfigDocument, error) {
	if err := payload.ValidateFor(profile); err != nil {
		return nil, err
	}
	var documents []persistence.ConfigDocument
	for resourceType, items := range payload.Resources {
		for _, item := range items {
			var object map[string]any
			if err := json.Unmarshal(item, &object); err != nil {
				return nil, fmt.Errorf("resource type %q contains a non-object item", resourceType)
			}
			if resourceType == "object_group" && profile.ID().String() == "orchestrator" && objectGroupKind(object) != "ip" {
				return nil, fmt.Errorf("resource type %q only supports IP groups for orchestrator", resourceType)
			}
			if strings.HasPrefix(resourceType, "security_") {
				if resourceType == "security_acl" && stringField(object, "id") == "sec-acl-default-deny-wan" {
					if degraded, _ := object["degraded"].(bool); degraded {
						// Collection defaults are presentation fallbacks, not desired
						// configuration. Never materialize one during import/restore.
						continue
					}
				}
				delete(object, "runtime_state")
				delete(object, "capabilities")
				if err := validateDesiredPayload(resourceType, object); err != nil {
					return nil, fmt.Errorf("resource type %q contains invalid security item: %w", resourceType, err)
				}
			}
			id := stringField(object, "id")
			if id == "" {
				return nil, fmt.Errorf("resource type %q item id is required", resourceType)
			}
			payload, hash, err := persistence.MarshalPayload(object)
			if err != nil {
				return nil, err
			}
			documents = append(documents, persistence.ConfigDocument{ResourceType: resourceType, ResourceID: id, Payload: payload, PayloadHash: hash, UpdatedAt: updatedAt})
		}
	}
	return documents, nil
}

func snapshotResponse(snapshot persistence.RuntimeSnapshot, productID product.ID) map[string]any {
	return map[string]any{"id": snapshot.ID, "product": productID, "source_transaction_id": snapshot.SourceTransactionID, "payload_hash": snapshot.PayloadHash, "created_at": snapshot.CreatedAt}
}

func snapshotID(name string) string {
	cleaned := strings.ToLower(strings.TrimSpace(name))
	cleaned = strings.ReplaceAll(cleaned, " ", "-")
	cleaned = strings.Trim(cleaned, "-/")
	if cleaned == "" {
		return "manual-" + newRequestID()
	}
	return "manual-" + cleaned + "-" + newRequestID()
}

func configManifest(payload ConfigPackagePayload) ConfigPackageManifest {
	return manifestForPayload(payload)
}

func (server *Server) validateImportConfirmation(req ConfigImportRequest, actor, packageHash, diffHash string) error {
	if strings.TrimSpace(req.ConfirmationToken) == "" {
		return fmt.Errorf("confirmation_token is required")
	}
	if strings.TrimSpace(req.ConfirmationActor) != actor {
		return fmt.Errorf("confirmation_actor must match authenticated actor")
	}
	if req.ConfirmationPackageHash != packageHash {
		return fmt.Errorf("package_hash confirmation does not match payload")
	}
	if req.ConfirmationDiffHash != diffHash {
		return fmt.Errorf("diff_hash confirmation does not match payload")
	}
	expiresAt, err := time.Parse(time.RFC3339, strings.TrimSpace(req.ConfirmationExpiresAt))
	if err != nil {
		return fmt.Errorf("confirmation_expires_at must be RFC3339")
	}
	if !server.now().UTC().Before(expiresAt) {
		return fmt.Errorf("confirmation token has expired")
	}
	return nil
}

func hashObject(value any) string {
	raw, _ := json.Marshal(value)
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func importPackageContainsSecret(req map[string]any) bool {
	if containsSecret(req) {
		return true
	}
	for _, key := range []string{"payload", "resources", "package"} {
		if containsSecret(req[key]) {
			return true
		}
	}
	return false
}

func containsSecret(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if secretToken(key) || containsSecret(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsSecret(child) {
				return true
			}
		}
	case string:
		return secretToken(typed)
	}
	return false
}

func secretToken(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	for _, token := range []string{"password", "passwd", "secret", "token", "private_key", "private-key", "credential", "vless://", "vmess://", "trojan://", "ss://", "xray_subscription"} {
		if strings.Contains(normalized, token) {
			return true
		}
	}
	return false
}

func (server *Server) handleTelemetry(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
			return
		}
		if _, ok := server.sessionFromRequest(r); !ok {
			writeError(w, r, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		payload, capabilities := server.telemetryPayload(r.Context(), kind)
		writeJSON(w, http.StatusOK, map[string]any{"data": payload, "capabilities": capabilities, "request_id": requestID(r)})
	}
}

func (server *Server) telemetryPayload(ctx context.Context, kind string) (any, []controlapi.CapabilityState) {
	components := []RuntimeComponentState{}
	runtimeStateValue := "degraded"
	plan, err := server.buildRuntimePlan(ctx, "telemetry")
	if err == nil {
		components = server.runtimeStatusComponents(ctx, plan.RuntimeArtifacts, len(plan.VppOperations) > 0)
		runtimeStateValue = runtimeState(components)
	}
	server.runtimeMu.Lock()
	lastApply := server.lastRuntime
	server.runtimeMu.Unlock()
	capabilities := []controlapi.CapabilityState{{Name: "local_telemetry", Available: true, State: controlapi.CapabilityAvailable}}
	if err != nil {
		capabilities = append(capabilities, controlapi.CapabilityState{Name: "runtime_status", Available: false, State: controlapi.CapabilityDegraded, Reason: err.Error()})
	}
	if payload, capability, handled := server.orchestratorTelemetryPayload(ctx, kind, runtimeStateValue, components, lastApply); handled {
		return payload, append(capabilities, capability)
	}
	switch kind {
	case "dashboard":
		if server.vppCounters != nil {
			payload, err := server.vppCounters.Dashboard(ctx)
			if err == nil {
				payload["device_mode"] = "gateway"
				payload["runtime_state"] = runtimeStateValue
				payload["degraded"] = false
				payload["components"] = components
				payload["last_apply"] = lastApply
				return payload, append(capabilities, controlapi.CapabilityState{Name: "vpp_counters", Available: true, State: controlapi.CapabilityAvailable})
			}
			capabilities = append(capabilities, controlapi.CapabilityState{Name: "vpp_counters", Available: false, State: controlapi.CapabilityDegraded, Reason: err.Error()})
		}
		if server.gatewayTelemetry != nil {
			if err := server.collectGatewayTelemetry(ctx); err == nil {
				sessions, throughput := server.gatewayDashboardCounters()
				return map[string]any{"device_mode": "gateway", "degraded": false, "runtime_state": runtimeStateValue, "active_path": "vpp", "components": components, "last_apply": lastApply, "sessions": sessions, "throughput_bps": throughput, "policy_hits": 0}, append(capabilities, controlapi.CapabilityState{Name: "gateway_vpp_telemetry", Available: true, State: controlapi.CapabilityAvailable})
			} else {
				capabilities = append(capabilities, controlapi.CapabilityState{Name: "gateway_vpp_telemetry", Available: false, State: controlapi.CapabilityDegraded, Reason: err.Error()})
			}
		}
		return map[string]any{"device_mode": "gateway", "degraded": runtimeStateValue != "running", "runtime_state": runtimeStateValue, "active_path": "vpp", "components": components, "last_apply": lastApply, "sessions": 0, "throughput_bps": 0, "policy_hits": 0}, capabilities
	case "interfaces":
		items, interfaceCapabilities, err := server.interfaceRuntimeSnapshot(ctx)
		if err != nil {
			return map[string]any{"items": []map[string]any{}, "runtime_state": runtimeStateValue, "degraded": true, "degraded_reason": err.Error()}, append(capabilities, controlapi.CapabilityState{Name: "interfaces", Available: false, State: controlapi.CapabilityDegraded, Reason: err.Error()})
		}
		return items, append(capabilities, interfaceCapabilities...)
	case "policy_hits":
		if server.policyHits != nil {
			items, err := server.policyHits.PolicyHits(ctx)
			if err == nil {
				return normalizePolicyHits(items, runtimeStateValue, "vpp_acl_lookup"), append(capabilities, controlapi.CapabilityState{Name: "vpp_policy_counters", Available: true, State: controlapi.CapabilityAvailable})
			}
			capabilities = append(capabilities, controlapi.CapabilityState{Name: "vpp_policy_counters", Available: false, State: controlapi.CapabilityDegraded, Reason: err.Error()})
		}
		if server.vppCounters != nil {
			items, err := server.vppCounters.PolicyHits(ctx)
			if err == nil {
				return normalizePolicyHits(items, runtimeStateValue, "vpp_counter_collector"), append(capabilities, controlapi.CapabilityState{Name: "vpp_counters", Available: true, State: controlapi.CapabilityAvailable})
			}
			capabilities = append(capabilities, controlapi.CapabilityState{Name: "vpp_counters", Available: false, State: controlapi.CapabilityDegraded, Reason: err.Error()})
		}
		items, receiptCapabilities := server.vppReceiptPolicyHits(runtimeStateValue)
		return items, append(capabilities, receiptCapabilities...)
	case "online_users":
		items, leaseCapabilities, err := server.dhcpLeaseSnapshot(ctx)
		if err != nil {
			return map[string]any{"items": []map[string]any{}, "runtime_state": runtimeStateValue, "degraded": true, "degraded_reason": err.Error()}, append(capabilities, controlapi.CapabilityState{Name: "kea_leases", Available: false, State: controlapi.CapabilityDegraded, Reason: err.Error()})
		}
		neighbors := []GatewayNeighbor{}
		neighborState := "unavailable"
		neighborReason := "gateway telemetry collector is not configured"
		if server.gatewayTelemetry != nil {
			neighbors, neighborState, neighborReason = server.gatewayNeighborSnapshot(ctx)
		}
		onlineUsers, trafficCapability := normalizeOnlineUsersWithNeighbors(onlineUserTelemetryInput{leases: items, neighbors: neighbors, runtimeState: runtimeStateValue, leaseDegraded: len(leaseCapabilities) == 0 || !leaseCapabilities[0].Available, neighborState: neighborState, neighborReason: neighborReason})
		payload := map[string]any{"items": onlineUsers, "runtime_state": runtimeStateValue, "degraded": len(leaseCapabilities) == 0 || !leaseCapabilities[0].Available || !trafficCapability.Available}
		if trafficCapability.Reason != "" {
			payload["degraded_reason"] = trafficCapability.Reason
		}
		return payload, append(append(capabilities, leaseCapabilities...), trafficCapability)
	case "top_sessions":
		if server.gatewayTelemetry != nil {
			items, state, reason := server.gatewayTopConnections(ctx)
			available := state == "available"
			capability := controlapi.CapabilityState{Name: "top_connections", Available: available, State: controlapi.CapabilityAvailable, Reason: reason}
			if !available {
				capability.State = controlapi.CapabilityDegraded
			}
			return map[string]any{"items": normalizeTopTelemetry(items, runtimeStateValue, !available, reason), "runtime_state": runtimeStateValue, "state": state, "degraded": !available, "degraded_reason": reason}, append(capabilities, capability)
		}
		if server.topTelemetry != nil {
			items, err := server.topTelemetry.TopSessions(ctx)
			if err == nil {
				return map[string]any{"items": normalizeTopTelemetry(items, runtimeStateValue, false, ""), "runtime_state": runtimeStateValue, "degraded": false}, append(capabilities, controlapi.CapabilityState{Name: "top_sessions", Available: true, State: controlapi.CapabilityAvailable})
			}
			reason := err.Error()
			capabilities = append(capabilities, controlapi.CapabilityState{Name: "top_sessions", Available: false, State: controlapi.CapabilityDegraded, Reason: reason})
			return map[string]any{"items": []map[string]any{}, "runtime_state": runtimeStateValue, "degraded": true, "degraded_reason": reason}, capabilities
		}
		capabilities = append(capabilities, controlapi.CapabilityState{Name: "top_sessions", Available: false, State: controlapi.CapabilityDegraded, Reason: "top session collector is not configured"})
		return map[string]any{"items": []map[string]any{}, "runtime_state": runtimeStateValue, "degraded": true, "degraded_reason": "top session collector is not configured"}, capabilities
	case "top_domains":
		if server.profile.ID() == product.Gateway().ID() {
			reason := "Gateway Top Domains is unavailable until a SmartDNS collector is configured"
			capabilities = append(capabilities, controlapi.CapabilityState{Name: "top_domains", Available: false, State: controlapi.CapabilityDegraded, Reason: reason})
			return map[string]any{"items": []map[string]any{}, "runtime_state": runtimeStateValue, "state": "unavailable", "degraded": true, "degraded_reason": reason}, capabilities
		}
		if server.topTelemetry != nil {
			items, err := server.topTelemetry.TopDomains(ctx)
			if err == nil {
				return map[string]any{"items": normalizeTopTelemetry(items, runtimeStateValue, false, ""), "runtime_state": runtimeStateValue, "degraded": false}, append(capabilities, controlapi.CapabilityState{Name: "top_domains", Available: true, State: controlapi.CapabilityAvailable})
			}
			reason := err.Error()
			capabilities = append(capabilities, controlapi.CapabilityState{Name: "top_domains", Available: false, State: controlapi.CapabilityDegraded, Reason: reason})
			return map[string]any{"items": []map[string]any{}, "runtime_state": runtimeStateValue, "degraded": true, "degraded_reason": reason}, capabilities
		}
		capabilities = append(capabilities, controlapi.CapabilityState{Name: "top_domains", Available: false, State: controlapi.CapabilityDegraded, Reason: "top domain collector is not configured"})
		return map[string]any{"items": []map[string]any{}, "runtime_state": runtimeStateValue, "degraded": true, "degraded_reason": "top domain collector is not configured"}, capabilities
	default:
		return map[string]any{"runtime_state": runtimeStateValue, "degraded": runtimeStateValue != "running"}, capabilities
	}
}

func (server *Server) interfaceRuntimeItem(ctx context.Context, id string) (map[string]any, bool, []controlapi.CapabilityState, error) {
	items, capabilities, err := server.interfaceRuntimeSnapshot(ctx)
	if err != nil {
		return nil, false, capabilities, err
	}
	resolved := server.resolveInterfaceID(ctx, id)
	for _, item := range items {
		if stringField(item, "id") == id || stringField(item, "name") == id || stringField(item, "display_name") == id || stringField(item, "alias") == id || stringField(item, "system_name") == id || stringField(item, "interface_id") == id || resolved == stringField(item, "system_name") {
			return item, true, capabilities, nil
		}
	}
	return nil, false, capabilities, nil
}

func (server *Server) interfaceRuntimeSnapshot(ctx context.Context) ([]map[string]any, []controlapi.CapabilityState, error) {
	discovered := hostInterfaceInventory()
	if server.interfaceTelemetry != nil {
		items, err := server.interfaceTelemetry.Interfaces(ctx)
		if err == nil {
			items = server.mergeInterfaceInventory(ctx, discovered, items)
			return server.normalizeInterfaceSnapshot(ctx, items, "running", "", false), []controlapi.CapabilityState{{Name: "vpp_interface_runtime", Available: true, State: controlapi.CapabilityAvailable}}, nil
		}
	}
	items, err := server.desiredItems(ctx, "interface")
	if err != nil {
		return nil, nil, err
	}
	if len(discovered) > 0 {
		items = server.mergeInterfaceInventory(ctx, discovered, items)
	}
	vppComponent := server.vppRuntimeComponent(ctx, true)
	state := vppComponent.State
	if state == "" {
		state = "degraded"
	}
	reason := vppComponent.Reason
	if vppComponent.Available {
		reason = "VPP interface counters unavailable from current apply receipt"
	}
	capability := controlapi.CapabilityState{Name: "vpp_interface_runtime", Available: vppComponent.Available, State: controlapi.CapabilityDegraded, Reason: reason}
	if !vppComponent.Available && state == "running" {
		capability.State = controlapi.CapabilityAvailable
	}
	return server.normalizeInterfaceSnapshot(ctx, items, state, reason, true), []controlapi.CapabilityState{capability}, nil
}

func (server *Server) appliedVPPDataplaneInterfaces() map[string]string {
	receipt, err := server.readVPPApplyReceipt()
	if err != nil || receipt.DryRun || (receipt.Status != "applied" && receipt.Status != "ready") {
		return nil
	}
	applied := map[string]string{}
	for _, operation := range receipt.Operations {
		if operation.Name != "vpp.dataplane.attach" || strings.TrimSpace(operation.Resource) == "" {
			continue
		}
		driver := "vpp_native"
		ok := len(operation.Results) > 0
		for _, result := range operation.Results {
			if result.Status != "applied" && result.Status != "ignored-failure" {
				ok = false
				break
			}
			if strings.TrimSpace(result.Hook) != "" {
				driver = strings.TrimSpace(result.Hook)
			} else if strings.TrimSpace(result.Driver) != "" {
				driver = strings.TrimSpace(result.Driver)
			}
		}
		if ok {
			applied[operation.Resource] = driver
		}
	}
	return applied
}

var hostInterfaceInventory = localInterfaceInventory

func (server *Server) mergeInterfaceInventory(ctx context.Context, primary, secondary []map[string]any) []map[string]any {
	merged := make([]map[string]any, 0, len(primary)+len(secondary))
	seen := map[string]int{}
	appendOrMerge := func(item map[string]any) {
		clone := cloneObject(item)
		key := nonEmpty(stringField(clone, "id"), stringField(clone, "name"))
		if key == "" {
			merged = append(merged, clone)
			return
		}
		if index, ok := seen[key]; ok {
			for k, v := range clone {
				if _, exists := merged[index][k]; !exists || merged[index][k] == "" || merged[index][k] == nil {
					merged[index][k] = v
				}
			}
			if _, vppRuntime := clone["vpp_interface"]; vppRuntime {
				for _, field := range []string{"vpp_interface", "active_path", "work_mode", "runtime_state", "link_state", "rx_bps", "tx_bps", "rx_pps", "tx_pps", "rx_bytes", "tx_bytes", "rx_packets", "tx_packets", "sessions"} {
					if value, exists := clone[field]; exists {
						merged[index][field] = value
					}
				}
			}
			return
		}
		seen[key] = len(merged)
		merged = append(merged, clone)
	}
	for _, item := range primary {
		appendOrMerge(item)
	}
	for _, item := range secondary {
		appendOrMerge(item)
	}
	return server.overlayInterfaceDesiredMetadata(ctx, merged)
}

func (server *Server) overlayInterfaceDesiredMetadata(ctx context.Context, runtimeItems []map[string]any) []map[string]any {
	desired, err := server.desiredItems(ctx, "interface")
	if err != nil || len(desired) == 0 {
		return runtimeItems
	}
	metadata := map[string]map[string]any{}
	for _, item := range desired {
		clone := cloneObject(item)
		for _, key := range []string{stringField(clone, "id"), stringField(clone, "name")} {
			if key != "" {
				metadata[key] = clone
			}
		}
	}
	merged := make([]map[string]any, 0, len(runtimeItems))
	matched := map[string]bool{}
	for _, item := range runtimeItems {
		clone := cloneObject(item)
		if desiredItem, ok := metadata[stringField(clone, "id")]; ok {
			applyInterfaceDesiredMetadata(clone, desiredItem)
			matched[stringField(desiredItem, "id")] = true
			matched[stringField(desiredItem, "name")] = true
		} else if desiredItem, ok := metadata[stringField(clone, "name")]; ok {
			applyInterfaceDesiredMetadata(clone, desiredItem)
			matched[stringField(desiredItem, "id")] = true
			matched[stringField(desiredItem, "name")] = true
		}
		merged = append(merged, clone)
	}
	for _, item := range desired {
		if matched[stringField(item, "id")] || matched[stringField(item, "name")] {
			continue
		}
		merged = append(merged, cloneObject(item))
	}
	return merged
}

func applyInterfaceDesiredMetadata(target, desired map[string]any) {
	for _, key := range []string{"display_name", "mode_role", "gateway_role", "role", "candidate_scopes"} {
		if value, ok := desired[key]; ok {
			target[key] = value
		}
	}
	if cidr := strings.TrimSpace(firstNonEmpty(stringField(desired, "cidr"), stringField(desired, "ip_cidr"), stringField(desired, "address"), stringField(desired, "ip"))); cidr != "" {
		if strings.TrimSpace(stringField(target, "cidr")) == "" {
			target["cidr"] = cidr
		}
		if strings.TrimSpace(firstNonEmpty(stringField(target, "address"), stringField(target, "ip"))) == "" {
			target["address"] = cidr
		}
	}
}

func (server *Server) normalizeInterfaceDesiredPayload(ctx context.Context, payload map[string]any, explicitID string) {
	requested := strings.TrimSpace(firstNonEmpty(explicitID, stringField(payload, "interface_id"), stringField(payload, "id"), stringField(payload, "name")))
	actual := server.resolveInterfaceID(ctx, requested)
	if actual == "" {
		actual = requested
	}
	payload["id"] = actual
	payload["interface_id"] = actual
	payload["system_name"] = actual
	payload["name"] = server.displayInterfaceName(ctx, actual)
}

func (server *Server) normalizeWANLinkDesiredPayload(ctx context.Context, payload map[string]any, explicitID string) {
	requested := strings.TrimSpace(firstNonEmpty(stringField(payload, "interface_id"), stringField(payload, "system_name")))
	if requested == "" && strings.TrimSpace(explicitID) != "" {
		if existing, found, err := server.desiredItem(ctx, "wan_link", strings.TrimSpace(explicitID)); err == nil && found {
			requested = strings.TrimSpace(firstNonEmpty(stringField(existing, "interface_id"), stringField(existing, "system_name")))
		}
	}
	actual := server.resolveInterfaceID(ctx, requested)
	if actual == "" {
		actual = requested
	}
	if actual == "" {
		return
	}
	payload["interface_id"] = actual
	payload["system_name"] = actual
	if strings.TrimSpace(stringField(payload, "display_name")) == "" {
		payload["display_name"] = server.displayInterfaceName(ctx, actual)
	}
}

func (server *Server) resolveInterfaceID(ctx context.Context, id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	for _, item := range server.interfaceAliasSnapshot(ctx) {
		actual := strings.TrimSpace(firstNonEmpty(stringField(item, "system_name"), stringField(item, "id"), stringField(item, "name")))
		if actual == "" {
			continue
		}
		if id == actual || id == server.displayInterfaceName(ctx, actual) {
			return actual
		}
	}
	return id
}

func (server *Server) displayInterfaceName(ctx context.Context, actual string) string {
	actual = strings.TrimSpace(actual)
	if actual == "" {
		return ""
	}
	items := server.interfaceAliasSnapshot(ctx)
	management := server.managementInterfaceID(ctx)
	ordered := make([]string, 0, len(items))
	if management != "" {
		ordered = append(ordered, management)
	}
	for _, item := range items {
		name := strings.TrimSpace(firstNonEmpty(stringField(item, "system_name"), stringField(item, "id"), stringField(item, "name")))
		if name == "" || name == management {
			continue
		}
		ordered = append(ordered, name)
	}
	for index, name := range ordered {
		if name == actual {
			return fmt.Sprintf("eth%d", index)
		}
	}
	return actual
}

func (server *Server) interfaceAliasSnapshot(ctx context.Context) []map[string]any {
	items := hostInterfaceInventory()
	sort.SliceStable(items, func(i, j int) bool {
		left := strings.TrimSpace(nonEmpty(stringField(items[i], "id"), stringField(items[i], "name")))
		right := strings.TrimSpace(nonEmpty(stringField(items[j], "id"), stringField(items[j], "name")))
		return left < right
	})
	return items
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func localInterfaceInventory() []map[string]any {
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return nil
	}
	items := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		name := strings.TrimSpace(entry.Name())
		if name == "" || name == "lo" {
			continue
		}
		if _, err := net.InterfaceByName(name); err != nil {
			continue
		}
		path := filepath.Join("/sys/class/net", name)
		if _, err := os.Stat(filepath.Join(path, "device")); err != nil && !envBoolOrDefault("LY_ROUTE_INCLUDE_VIRTUAL_INTERFACES", false) {
			continue
		}
		mtu := 1500
		if data, err := os.ReadFile(filepath.Join(path, "mtu")); err == nil {
			if parsed, parseErr := strconv.Atoi(strings.TrimSpace(string(data))); parseErr == nil && parsed > 0 {
				mtu = parsed
			}
		}
		linkState := "unknown"
		if data, err := os.ReadFile(filepath.Join(path, "operstate")); err == nil {
			linkState = strings.TrimSpace(string(data))
		}
		items = append(items, map[string]any{"id": name, "name": name, "admin_state": "up", "link_state": linkState, "speed": localInterfaceSpeed(path), "mtu": mtu, "mac": localInterfaceMAC(name), "addresses": localInterfaceAddresses(name), "work_mode": "kernel_stack", "active_path": "kernel_stack", "mode_role": map[string]any{"gateway": nil, "bridge": nil}, "candidate_scopes": []string{"lan", "wan", "internal", "external"}, "capability": defaultDatapathCapability()})
	}
	return items
}

func localInterfaceMAC(name string) string {
	iface, err := net.InterfaceByName(name)
	if err != nil || iface == nil {
		return ""
	}
	return iface.HardwareAddr.String()
}

func localInterfaceAddresses(name string) []string {
	iface, err := net.InterfaceByName(name)
	if err != nil || iface == nil {
		return nil
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return nil
	}
	values := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		if addr != nil {
			values = append(values, addr.String())
		}
	}
	return values
}

func localInterfaceSpeed(path string) string {
	data, err := os.ReadFile(filepath.Join(path, "speed"))
	if err != nil {
		return "unknown"
	}
	speed := strings.TrimSpace(string(data))
	if speed == "" || strings.HasPrefix(speed, "-") {
		return "unknown"
	}
	return speed + "Mb/s"
}

func (server *Server) dhcpLeaseSnapshot(ctx context.Context) ([]map[string]any, []controlapi.CapabilityState, error) {
	if server.dhcpLeases != nil {
		items, err := server.dhcpLeases.Leases(ctx)
		if err == nil {
			return normalizeDHCPLeases(items, "running", "", false), []controlapi.CapabilityState{{Name: "kea_leases", Available: true, State: controlapi.CapabilityAvailable}}, nil
		}
		return nil, []controlapi.CapabilityState{{Name: "kea_leases", Available: false, State: controlapi.CapabilityDegraded, Reason: err.Error()}}, err
	}
	items, err := server.desiredItems(ctx, "dhcp_lease")
	if err != nil {
		return nil, nil, err
	}
	reason := "Kea lease collector is not configured"
	return normalizeDHCPLeases(items, "degraded", reason, true), []controlapi.CapabilityState{{Name: "kea_leases", Available: false, State: controlapi.CapabilityDegraded, Reason: reason}}, nil
}

func normalizeDHCPLeases(items []map[string]any, state, reason string, degraded bool) []map[string]any {
	leases := make([]map[string]any, 0, len(items))
	for _, item := range items {
		clone := cloneObject(item)
		clone["runtime_state"] = state
		clone["degraded"] = degraded
		if reason != "" {
			clone["degraded_reason"] = reason
		}
		if stringField(clone, "id") == "" {
			if id := firstStringField(clone, "ip_address", "address", "mac", "hw_address"); id != "" {
				clone["id"] = id
			}
		}
		leases = append(leases, clone)
	}
	return leases
}

func normalizeOnlineUsers(leases []map[string]any, state string, leaseDegraded bool) ([]map[string]any, controlapi.CapabilityState) {
	users := make([]map[string]any, 0, len(leases))
	trafficAvailable := false
	for _, lease := range leases {
		clone := cloneObject(lease)
		ip := firstStringField(clone, "ip_address", "address", "ip")
		mac := firstStringField(clone, "mac", "hw_address", "hwaddr")
		if ip != "" {
			clone["ip"] = ip
			clone["ip_address"] = ip
		}
		if mac != "" {
			clone["mac"] = mac
		}
		if hostname := firstStringField(clone, "hostname", "client_hostname", "name"); hostname != "" {
			clone["hostname"] = hostname
		}
		copyStringAlias(clone, "lease_start", "lease_start", "starts", "valid_from", "cltt")
		copyStringAlias(clone, "lease_end", "lease_end", "expires", "valid_until", "expire_time")
		copyStringAlias(clone, "last_traffic_time", "last_traffic_time", "last_seen", "last_activity")
		if status := firstStringField(clone, "online_status", "status", "state"); status != "" && status != "running" && status != "degraded" {
			clone["online_status"] = status
		} else if leaseDegraded {
			clone["online_status"] = "unknown"
		} else {
			clone["online_status"] = "online"
		}
		hasTraffic := copyNumberAlias(clone, "rx_bps", "rx_bps", "in_bps", "download_bps")
		hasTraffic = copyNumberAlias(clone, "tx_bps", "tx_bps", "out_bps", "upload_bps") || hasTraffic
		hasTraffic = copyNumberAlias(clone, "rx_bytes", "rx_bytes", "in_bytes", "download_bytes", "bytes_rx") || hasTraffic
		hasTraffic = copyNumberAlias(clone, "tx_bytes", "tx_bytes", "out_bytes", "upload_bytes", "bytes_tx") || hasTraffic
		if _, ok := clone["last_traffic_time"]; ok {
			hasTraffic = true
		}
		if hasTraffic {
			trafficAvailable = true
			clone["traffic_activity_state"] = "available"
		} else {
			clone["rx_bps"] = 0
			clone["tx_bps"] = 0
			clone["rx_bytes"] = 0
			clone["tx_bytes"] = 0
			clone["traffic_activity_state"] = "degraded"
			clone["traffic_activity_reason"] = "recent VPP per-user traffic activity is not configured"
		}
		clone["runtime_state"] = state
		users = append(users, clone)
	}
	if trafficAvailable {
		return users, controlapi.CapabilityState{Name: "vpp_user_traffic", Available: true, State: controlapi.CapabilityAvailable}
	}
	return users, controlapi.CapabilityState{Name: "vpp_user_traffic", Available: false, State: controlapi.CapabilityDegraded, Reason: "recent VPP per-user traffic activity is not configured"}
}

func copyStringAlias(item map[string]any, target string, keys ...string) bool {
	value := firstStringField(item, keys...)
	if value == "" {
		return false
	}
	item[target] = value
	return true
}

func copyNumberAlias(item map[string]any, target string, keys ...string) bool {
	value, ok := numberField(item, keys...)
	if !ok {
		return false
	}
	item[target] = value
	return true
}

func normalizeTopTelemetry(items []map[string]any, state string, degraded bool, reason string) []map[string]any {
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		clone := cloneObject(item)
		clone["runtime_state"] = state
		clone["degraded"] = degraded
		if reason != "" {
			clone["degraded_reason"] = reason
		}
		result = append(result, clone)
	}
	return result
}

func (server *Server) normalizeInterfaceSnapshot(ctx context.Context, items []map[string]any, state, reason string, degraded bool) []map[string]any {
	managementInterface := server.managementInterfaceID(ctx)
	appliedVPP := server.appliedVPPDataplaneInterfaces()
	desiredCIDRs := server.desiredInterfaceCIDRs(ctx)
	snapshot := make([]map[string]any, 0, len(items))
	for _, item := range items {
		clone := cloneObject(item)
		name := strings.TrimSpace(nonEmpty(stringField(clone, "id"), stringField(clone, "name")))
		if cidr := strings.TrimSpace(desiredCIDRs[name]); cidr != "" {
			if strings.TrimSpace(firstNonEmpty(stringField(clone, "cidr"), stringField(clone, "ip_cidr"), stringField(clone, "ip"))) == "" {
				clone["cidr"] = cidr
			}
			if strings.TrimSpace(stringField(clone, "address")) == "" {
				clone["address"] = cidr
			}
		}
		displayName := server.displayInterfaceName(ctx, name)
		if displayName != "" {
			clone["system_name"] = name
			clone["id"] = displayName
			clone["name"] = displayName
		}
		if name == managementInterface {
			clone["active_path"] = "kernel_stack"
			clone["work_mode"] = "kernel_stack"
			clone["gateway_role"] = "management"
			clone["mode_role"] = map[string]any{"gateway": "management", "bridge": nil}
		} else if !degraded && strings.TrimSpace(stringField(clone, "active_path")) != "" && stringField(clone, "active_path") != "kernel_stack" {
			if strings.TrimSpace(stringField(clone, "work_mode")) == "" {
				clone["work_mode"] = stringField(clone, "active_path")
			}
		} else if driver := strings.TrimSpace(appliedVPP[name]); driver != "" {
			clone["active_path"] = driver
			clone["work_mode"] = driver
			clone["vpp_interface"] = "lyroute-" + name
		} else {
			clone["active_path"] = "kernel_stack"
			clone["work_mode"] = "kernel_stack"
		}
		if stringField(clone, "gateway_role") == "" {
			if modeRole, ok := clone["mode_role"].(map[string]any); ok {
				if gatewayRole, ok := modeRole["gateway"].(string); ok && strings.TrimSpace(gatewayRole) != "" {
					clone["gateway_role"] = strings.TrimSpace(gatewayRole)
				}
			}
		}
		for _, key := range []string{"rx_bps", "tx_bps", "rx_pps", "tx_pps", "sessions"} {
			if _, ok := clone[key]; !ok {
				clone[key] = 0
			}
		}
		clone["stats"] = map[string]any{"rx_bps": clone["rx_bps"], "tx_bps": clone["tx_bps"], "rx_pps": clone["rx_pps"], "tx_pps": clone["tx_pps"], "sessions": clone["sessions"]}
		clone["runtime_state"] = state
		clone["degraded"] = degraded
		if reason != "" {
			clone["degraded_reason"] = reason
		} else {
			delete(clone, "degraded_reason")
		}
		snapshot = append(snapshot, clone)
	}
	return snapshot
}

func (server *Server) handleAuditEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	if _, ok := server.sessionFromRequest(r); !ok {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	events, err := server.auditEvents(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "audit_read_failed", "audit events are unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": events, "request_id": requestID(r)})
}

func (server *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	if capability, known := routeCapability(r.URL.Path); known && !server.profile.Allows(capability) {
		writeCapabilityError(w, r, capability)
		return
	}
	writeError(w, r, http.StatusNotFound, "not_found", "unknown API route")
}

func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.Header.Get(HeaderRequestID))
		if id == "" {
			id = newRequestID()
		}
		ctx := context.WithValue(r.Context(), requestIDKey{}, id)
		w.Header().Set(HeaderRequestID, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type requestIDKey struct{}

func requestID(r *http.Request) string {
	if id, ok := r.Context().Value(requestIDKey{}).(string); ok && id != "" {
		return id
	}
	return strings.TrimSpace(r.Header.Get(HeaderRequestID))
}

func (server *Server) sessionFromRequest(r *http.Request) (Session, bool) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		if session, ok := server.sessions.get(cookie.Value); ok {
			return session, true
		}
	}
	if token := bearerToken(r.Header.Get("Authorization")); token != "" {
		return server.sessions.get(token)
	}
	return Session{}, false
}

func (server *Server) recordAudit(actor, role, resource, action, status, eventError string, r *http.Request) {
	eventError = auditSafeText(eventError)
	server.auditMu.Lock()
	defer server.auditMu.Unlock()
	event := AuditEvent{
		ID:        newRequestID(),
		Timestamp: server.now().UTC(),
		Actor:     actor,
		Role:      role,
		Resource:  resource,
		Action:    action,
		Status:    status,
		Error:     eventError,
		RequestID: requestID(r),
	}
	server.audit = append(server.audit, event)
	if server.store != nil {
		_ = server.store.SaveAuditEvents(r.Context(), []persistence.AuditEvent{{
			ID:            event.ID,
			Timestamp:     event.Timestamp,
			Actor:         event.Actor,
			Role:          event.Role,
			Resource:      event.Resource,
			Action:        event.Action,
			Status:        event.Status,
			Error:         event.Error,
			TransactionID: event.RequestID,
		}})
	}
}

func (server *Server) auditEvents(ctx context.Context) ([]AuditEvent, error) {
	if server.store != nil {
		persisted, err := server.store.AuditEvents(ctx, "")
		if err != nil {
			return nil, err
		}
		events := make([]AuditEvent, 0, len(persisted))
		for _, event := range persisted {
			events = append(events, AuditEvent{
				ID:        event.ID,
				Timestamp: event.Timestamp,
				Actor:     event.Actor,
				Role:      event.Role,
				Resource:  event.Resource,
				Action:    event.Action,
				Status:    event.Status,
				Error:     auditSafeText(event.Error),
				RequestID: event.TransactionID,
			})
		}
		return events, nil
	}
	server.auditMu.Lock()
	defer server.auditMu.Unlock()
	events := make([]AuditEvent, len(server.audit))
	copy(events, server.audit)
	return events, nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// The status and headers are already committed. A client disconnect is not a
	// server fault and must not turn into a recovered net/http panic.
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeJSON(w, status, ErrorResponse{Error: ErrorBody{Code: code, Message: message, RequestID: requestID(r)}})
}

func writeCapabilityError(w http.ResponseWriter, r *http.Request, capability product.Capability) {
	writeJSON(w, http.StatusNotFound, ErrorResponse{Error: ErrorBody{
		Code:       "capability_not_supported",
		Message:    "resource is not supported by the active product",
		RequestID:  requestID(r),
		Capability: capability,
	}})
}

func decodeStrictJSON(r *http.Request, out any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(out)
}

func newRequestID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		panic(fmt.Errorf("request id: %w", err))
	}
	return hex.EncodeToString(bytes[:])
}

func ListenAndServe(addr string, server *Server) error {
	if server == nil {
		server = New()
	}
	err := http.ListenAndServe(addr, server.Handler())
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
