package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/trevoraspencer/droid-proxy/internal/config"
)

// healthProbe is one GET /health attempt against a loopback address.
type healthProbe struct {
	Addr    string // host:port the probe targeted, e.g. "127.0.0.1:8787" or "[::1]:8787"
	OK      bool   // something answered HTTP
	Service string // "service" field of the /health JSON; empty for foreign responders
	Version string
	Err     error // connection-level failure when nothing answered
}

// doctorProbeListen is a seam so doctor tests never open sockets.
var doctorProbeListen = defaultProbeListen

func probeHealth(client *http.Client, baseURL string) healthProbe {
	p := healthProbe{Addr: strings.TrimPrefix(baseURL, "http://")}
	resp, err := client.Get(baseURL + "/health")
	if err != nil {
		p.Err = err
		return p
	}
	defer resp.Body.Close()
	p.OK = true
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return p
	}
	var payload struct {
		Service string `json:"service"`
		Version string `json:"version"`
	}
	if json.Unmarshal(body, &payload) == nil {
		p.Service = payload.Service
		p.Version = payload.Version
	}
	return p
}

// defaultProbeListen probes the configured listen address and, when that
// address is IPv4 loopback (droid-proxy's default bind), also [::1] on the
// same port — "localhost" resolves there first on macOS, so a foreign IPv6
// listener shadows the proxy for anything using a localhost URL.
func defaultProbeListen(cfg *config.Config) (primary, v6 healthProbe) {
	client := &http.Client{Timeout: time.Second}
	host := strings.TrimSpace(cfg.Listen.Host)
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	port := strconv.Itoa(cfg.Listen.Port)
	primary = probeHealth(client, "http://"+net.JoinHostPort(host, port))
	if ip := net.ParseIP(host); ip != nil && ip.To4() != nil && ip.IsLoopback() {
		v6 = probeHealth(client, "http://"+net.JoinHostPort("::1", port))
	} else {
		v6 = healthProbe{Addr: net.JoinHostPort("::1", port), Err: fmt.Errorf("skipped")}
	}
	return primary, v6
}

// writeListenProbe prints probe results and returns any hard issues.
// Existing doctor lines are untouched; this only appends new ones.
func writeListenProbe(out io.Writer, primary, v6 healthProbe, expectRunning bool) []string {
	var hard []string
	switch {
	case primary.OK && primary.Service == "droid-proxy":
		fmt.Fprintf(out, "health probe (%s): ok droid-proxy %s\n", primary.Addr, primary.Version)
	case primary.OK:
		msg := fmt.Sprintf("health probe: issue: another server answered on %s (service=%q) — port conflict with droid-proxy's configured listen address", primary.Addr, primary.Service)
		fmt.Fprintln(out, msg)
		hard = append(hard, msg)
	case expectRunning:
		msg := fmt.Sprintf("health probe: issue: %s not responding although the proxy reports running", primary.Addr)
		fmt.Fprintln(out, msg)
		hard = append(hard, msg)
	default:
		fmt.Fprintf(out, "health probe (%s): not responding\n", primary.Addr)
	}

	// droid-proxy binds IPv4 loopback only, so ANY responder on [::1] is a
	// foreign process shadowing "localhost" URLs. Soft warning: the proxy
	// itself is unaffected as long as clients use 127.0.0.1.
	if v6.OK {
		fmt.Fprintf(out, "warning: a different server is listening on %s — \"localhost\" may resolve to it; use http://%s for checks (known squatters: Cursor MCP OAuth loopback, wrangler dev)\n", v6.Addr, primary.Addr)
	}
	return hard
}
