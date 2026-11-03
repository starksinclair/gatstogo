package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"
)

// healthCheck is one dependency /healthz probes. ping is a func rather than
// an interface so each dependency (two pgx pools, Redis) plugs in with its
// own Ping method, and a test can supply a fake without a real server.
type healthCheck struct {
	name string
	ping func(context.Context) error
}

// healthTimeout bounds the whole probe, not each check: a load balancer or
// Cloud Run health probe should get an answer quickly even when a
// dependency is hanging, and a hung dependency is itself a failure.
const healthTimeout = 3 * time.Second

// healthHandler backs GET/HEAD /healthz for load balancers, Cloud Run
// startup/liveness probes, and uptime monitors. It reports 200 only when
// every dependency answers, otherwise 503 with the names (never the error
// text -- that can carry hostnames or credentials) of what is failing, so
// an on-call person knows where to look without the endpoint leaking
// anything to the public internet. It is deliberately host-agnostic:
// probes hit an IP or an internal name, not a tenant subdomain.
func healthHandler(checks []healthCheck) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), healthTimeout)
		defer cancel()

		var failing []string
		for _, c := range checks {
			if err := c.ping(ctx); err != nil {
				log.Println("healthz:", c.name, "check failed:", err)
				failing = append(failing, c.name)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if len(failing) > 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
			if r.Method != http.MethodHead {
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "unavailable", "failing": failing})
			}
			return
		}
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		}
	}
}

// serverHealthChecks wires /healthz to this server's real dependencies.
// The closures look the pools up at probe time, so a nil dependency (a
// partially configured dev setup) reports as failing instead of panicking.
func serverHealthChecks(s *Server) []healthCheck {
	return []healthCheck{
		{"database", func(ctx context.Context) error { return s.AppDB.Ping(ctx) }},
		{"admin_database", func(ctx context.Context) error { return s.AdminDB.Ping(ctx) }},
		{"redis", func(ctx context.Context) error { return s.Redis.Ping(ctx).Err() }},
	}
}
