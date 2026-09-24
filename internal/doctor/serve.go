package doctor

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"path/filepath"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/serve"
)

// ServeChecks evaluates the health of a relevo serve installation. It runs when
// the machine database holds the serve.tls.key secret (P5 §4.7): the
// certificate and the clients come from that database, and the state check
// probes the root the running daemon's serve.daemon row names -- or serveRoot
// when there is none. Reading the root from the row is what makes the serve
// rows show on a box started with a non-default --state.
func ServeChecks(env Env, d *db.DB, serveRoot string, now time.Time) []Check {
	if d == nil {
		return nil
	}
	if _, ok, err := d.SecretGet("serve.tls.key"); err != nil || !ok {
		return nil
	}

	root := serveRoot
	if p, ok, err := serve.ReadDaemonPointer(d); err == nil && ok && p.Root != "" {
		root = p.Root
	}

	var checks []Check

	// 1. Certificate check
	if rawCrt, ok, err := d.SecretGet("serve.tls.cert"); err != nil || !ok {
		checks = append(checks, Check{
			Group:    "serve",
			Name:     "certificate",
			Severity: SevFail,
			Detail:   "unreadable",
			Fix:      "relevo serve init",
		})
	} else {
		block, _ := pem.Decode(rawCrt)
		if block == nil {
			checks = append(checks, Check{
				Group:    "serve",
				Name:     "certificate",
				Severity: SevFail,
				Detail:   "unreadable",
				Fix:      "relevo serve init",
			})
		} else {
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				checks = append(checks, Check{
					Group:    "serve",
					Name:     "certificate",
					Severity: SevFail,
					Detail:   "unreadable",
					Fix:      "relevo serve init",
				})
			} else if now.After(cert.NotAfter) {
				checks = append(checks, Check{
					Group:    "serve",
					Name:     "certificate",
					Severity: SevFail,
					Detail:   "expired",
					Fix:      "relevo serve init",
				})
			} else if cert.NotAfter.Before(now.Add(30 * 24 * time.Hour)) {
				checks = append(checks, Check{
					Group:    "serve",
					Name:     "certificate",
					Severity: SevWarn,
					Detail:   fmt.Sprintf("expires %s", cert.NotAfter.Format("2006-01-02")),
				})
			} else {
				checks = append(checks, Check{
					Group:    "serve",
					Name:     "certificate",
					Severity: SevOK,
					Detail:   fmt.Sprintf("valid until %s", cert.NotAfter.Format("2006-01-02")),
				})
			}
		}
	}

	// 2. Clients check
	rawClients, clientsOK, err := d.KVGet("serve.clients")
	if err != nil {
		checks = append(checks, Check{
			Group:    "serve",
			Name:     "clients",
			Severity: SevFail,
			Detail:   err.Error(),
		})
	} else if !clientsOK {
		checks = append(checks, Check{
			Group:    "serve",
			Name:     "clients",
			Severity: SevWarn,
			Detail:   "none enrolled",
			Fix:      "relevo serve enroll --label <name> --key <line>",
		})
	} else {
		var list []struct {
			ID        string    `json:"id"`
			RevokedAt time.Time `json:"revoked_at,omitempty"`
		}
		if err := json.Unmarshal(rawClients, &list); err != nil {
			checks = append(checks, Check{
				Group:    "serve",
				Name:     "clients",
				Severity: SevFail,
				Detail:   "parse error",
				Fix:      "fix the serve.clients row in the database",
			})
		} else {
			active := 0
			for _, c := range list {
				if c.RevokedAt.IsZero() {
					active++
				}
			}
			if active == 0 {
				checks = append(checks, Check{
					Group:    "serve",
					Name:     "clients",
					Severity: SevWarn,
					Detail:   "none enrolled",
					Fix:      "relevo serve enroll --label <name> --key <line>",
				})
			} else if active == 1 {
				checks = append(checks, Check{
					Group:    "serve",
					Name:     "clients",
					Severity: SevOK,
					Detail:   "1 enrolled",
				})
			} else {
				checks = append(checks, Check{
					Group:    "serve",
					Name:     "clients",
					Severity: SevOK,
					Detail:   fmt.Sprintf("%d enrolled", active),
				})
			}
		}
	}

	// 3. State check
	bindingsDir := filepath.Join(root, "bindings")
	if err := env.Stat(root); err != nil {
		checks = append(checks, Check{
			Group:    "serve",
			Name:     "state",
			Severity: SevFail,
			Detail:   err.Error(),
			Fix:      "relevo serve init",
		})
	} else if err := env.Probe(bindingsDir); err != nil {
		checks = append(checks, Check{
			Group:    "serve",
			Name:     "state",
			Severity: SevFail,
			Detail:   fmt.Sprintf("not writable: %v", err),
		})
	} else {
		checks = append(checks, Check{
			Group:    "serve",
			Name:     "state",
			Severity: SevOK,
			Detail:   "bindings writable",
		})
	}

	return checks
}
