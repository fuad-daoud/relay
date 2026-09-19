package doctor

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ServeChecks evaluates the health of a relay serve installation.
// If serveRoot/server.key is absent, no checks are returned (this machine is not a server).
func ServeChecks(env Env, serveRoot string, now time.Time) []Check {
	keyPath := filepath.Join(serveRoot, "server.key")
	if err := env.Stat(keyPath); err != nil {
		return nil
	}

	var checks []Check

	// 1. Certificate check
	crtPath := filepath.Join(serveRoot, "server.crt")
	rawCrt, err := env.ReadFile(crtPath)
	if err != nil {
		checks = append(checks, Check{
			Group:    "serve",
			Name:     "certificate",
			Severity: SevFail,
			Detail:   "unreadable",
			Fix:      "relay serve init",
		})
	} else {
		block, _ := pem.Decode(rawCrt)
		if block == nil {
			checks = append(checks, Check{
				Group:    "serve",
				Name:     "certificate",
				Severity: SevFail,
				Detail:   "unreadable",
				Fix:      "relay serve init",
			})
		} else {
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				checks = append(checks, Check{
					Group:    "serve",
					Name:     "certificate",
					Severity: SevFail,
					Detail:   "unreadable",
					Fix:      "relay serve init",
				})
			} else if now.After(cert.NotAfter) {
				checks = append(checks, Check{
					Group:    "serve",
					Name:     "certificate",
					Severity: SevFail,
					Detail:   "expired",
					Fix:      "relay serve init",
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
	clientsPath := filepath.Join(serveRoot, "clients.json")
	rawClients, err := env.ReadFile(clientsPath)
	if err != nil {
		if errorsIsNotExist(err) {
			checks = append(checks, Check{
				Group:    "serve",
				Name:     "clients",
				Severity: SevWarn,
				Detail:   "none enrolled",
				Fix:      "relay serve enroll --label <name> --key <line>",
			})
		} else {
			checks = append(checks, Check{
				Group:    "serve",
				Name:     "clients",
				Severity: SevFail,
				Detail:   err.Error(),
			})
		}
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
				Fix:      "fix JSON formatting in " + clientsPath,
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
					Fix:      "relay serve enroll --label <name> --key <line>",
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
	bindingsDir := filepath.Join(serveRoot, "bindings")
	if err := env.Stat(serveRoot); err != nil {
		checks = append(checks, Check{
			Group:    "serve",
			Name:     "state",
			Severity: SevFail,
			Detail:   err.Error(),
			Fix:      "relay serve init",
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

func errorsIsNotExist(err error) bool {
	return os.IsNotExist(err)
}
