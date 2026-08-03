package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"ly-route/backend/internal/runtime/pppoeclient"
)

func main() {
	configFile := flag.String("config", "", "native PPPoE client JSON config")
	interfaceName := flag.String("interface", "", "Linux control-plane interface connected to VPP")
	username := flag.String("username", "", "PPPoE username")
	password := flag.String("password", "", "PPPoE password")
	mru := flag.Uint("mru", 1492, "PPPoE MRU")
	timeout := flag.Duration("timeout", 3*time.Second, "control packet timeout")
	retries := flag.Int("retries", 4, "negotiation retries")
	vppctl := flag.String("vppctl", "vppctl", "VPP CLI binary")
	tableID := flag.Int("table-id", 0, "VPP table ID")
	defaultRoute := flag.Bool("default-route", true, "install the VPP default route")
	nat := flag.Bool("nat", true, "enable NAT44 outside on the PPPoE session")
	wanInterface := flag.String("wan-interface", "", "VPP WAN interface; when set, prepare the VPP control-plane tap")
	tapID := flag.Int("tap-id", 700, "VPP control-plane tap ID")
	statusFile := flag.String("status-file", "", "runtime status JSON path")
	natInsideInterfaces := []string{}
	ipv6LANInterfaces := []string{}
	ipv6PrefixGroup := ""
	flag.Parse()
	if *configFile != "" {
		content, err := os.ReadFile(*configFile)
		if err != nil {
			fatal(err)
		}
		var config struct {
			ControlInterface    string   `json:"control_interface"`
			WANInterface        string   `json:"wan_interface"`
			Username            string   `json:"username"`
			Password            string   `json:"password"`
			VPPCTL              string   `json:"vppctl"`
			StatusFile          string   `json:"status_file"`
			MRU                 uint     `json:"mru"`
			Retries             int      `json:"retries"`
			TapID               int      `json:"tap_id"`
			TableID             int      `json:"table_id"`
			DefaultRoute        *bool    `json:"default_route"`
			NAT                 *bool    `json:"nat"`
			NATInsideInterfaces []string `json:"nat_inside_interfaces"`
			IPv6LANInterfaces   []string `json:"ipv6_lan_interfaces"`
			IPv6PrefixGroup     string   `json:"ipv6_prefix_group"`
		}
		if err := json.Unmarshal(content, &config); err != nil {
			fatal(err)
		}
		*interfaceName, *wanInterface = config.ControlInterface, config.WANInterface
		*username, *password = config.Username, config.Password
		if config.VPPCTL != "" {
			*vppctl = config.VPPCTL
		}
		if config.StatusFile != "" {
			*statusFile = config.StatusFile
		}
		if config.MRU != 0 {
			*mru = config.MRU
		}
		if config.Retries != 0 {
			*retries = config.Retries
		}
		if config.TapID != 0 {
			*tapID = config.TapID
		}
		*tableID = config.TableID
		if config.DefaultRoute != nil {
			*defaultRoute = *config.DefaultRoute
		}
		if config.NAT != nil {
			*nat = *config.NAT
		}
		natInsideInterfaces = append(natInsideInterfaces, config.NATInsideInterfaces...)
		ipv6LANInterfaces = append(ipv6LANInterfaces, config.IPv6LANInterfaces...)
		ipv6PrefixGroup = config.IPv6PrefixGroup
	}
	if len(natInsideInterfaces) == 0 {
		for _, name := range strings.Split(os.Getenv("LY_ROUTE_LAN_INTERFACE"), ",") {
			name = strings.TrimSpace(name)
			if name != "" {
				if !strings.HasPrefix(name, "lyroute-") {
					name = "lyroute-" + name
				}
				natInsideInterfaces = append(natInsideInterfaces, name)
			}
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	var control pppoeclient.ControlPlane
	var err error
	if *wanInterface != "" {
		if *interfaceName == "" {
			*interfaceName = fmt.Sprintf("ly-pppoe-%d", *tapID)
		}
		control, err = pppoeclient.PrepareControlPlane(ctx, pppoeclient.ControlPlaneConfig{VPPCTL: *vppctl, WANInterface: *wanInterface, HostInterface: *interfaceName, TapID: *tapID})
		if err != nil {
			fatal(err)
		}
		defer control.Remove(context.Background())
	}
	link, err := pppoeclient.OpenRawLink(*interfaceName)
	if err != nil {
		fatal(err)
	}
	defer link.Close()
	client, err := pppoeclient.New(pppoeclient.Config{Interface: *interfaceName, Username: *username, Password: *password, MRU: uint16(*mru), Timeout: *timeout, Retries: *retries}, link)
	if err != nil {
		fatal(err)
	}
	session, err := client.Connect(ctx)
	if err != nil {
		fatal(err)
	}
	encoded, _ := json.Marshal(session)
	programmed, err := pppoeclient.ProgramVPP(ctx, pppoeclient.VPPConfig{Binary: *vppctl, TableID: *tableID, MTU: uint16(*mru), InstallDefaultRoute: *defaultRoute, EnableNAT: *nat, NATInsideInterfaces: natInsideInterfaces, IPv6PrefixGroup: ipv6PrefixGroup, IPv6LANInterfaces: ipv6LANInterfaces}, session)
	if err != nil {
		fatal(err)
	}
	defer programmed.Remove(context.Background())
	encoded, _ = json.Marshal(programmed)
	fmt.Println(string(encoded))
	writeStatus(*statusFile, map[string]any{"state": "connected", "interface": programmed.Interface, "session": session})
	defer writeStatus(*statusFile, map[string]any{"state": "disconnected"})
	defer client.Disconnect(context.Background())
	if err := client.Serve(ctx); err != nil && ctx.Err() == nil {
		fatal(err)
	}
}

func writeStatus(path string, value any) {
	if path == "" {
		return
	}
	content, err := json.Marshal(value)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return
	}
	temporary := path + ".tmp"
	if os.WriteFile(temporary, append(content, '\n'), 0600) == nil {
		_ = os.Rename(temporary, path)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
