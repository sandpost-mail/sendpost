package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sandpost-mail/sandpost/internal/audit"
	"github.com/sandpost-mail/sandpost/internal/authz"
	"github.com/sandpost-mail/sandpost/internal/policy"
	"github.com/sandpost-mail/sandpost/internal/store"
	"github.com/sandpost-mail/sandpost/internal/store/sqlcgen"
)

// Transport kinds.
const (
	KindSMTP = "smtp"
	KindSES  = "ses"
)

const maxFieldLength = 255

var hostRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)

// Transport is the stored relay of a project. It never carries the password,
// only whether one is stored.
type Transport struct {
	Kind      string    `json:"kind"`
	Host      string    `json:"host"`
	Port      int       `json:"port"`
	Username  string    `json:"username"`
	HasSecret bool      `json:"has_secret"`
	TLSMode   string    `json:"tls_mode"`
	Sender    string    `json:"sender"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TransportInput is a request to create or replace the transport.
//
// Secret keeps the stored password when nil and the connection settings did
// not change, clears it when empty, and replaces it otherwise.
type TransportInput struct {
	Kind     string  `json:"kind"`
	Host     string  `json:"host"`
	Port     int     `json:"port"`
	Username string  `json:"username"`
	Secret   *string `json:"secret"`
	TLSMode  string  `json:"tls_mode"`
	Sender   string  `json:"sender"`
	Enabled  bool    `json:"enabled"`
}

func toTransport(r sqlcgen.Transport) Transport {
	return Transport{
		Kind: r.Kind, Host: r.Host, Port: int(r.Port), Username: r.Username, HasSecret: len(r.SecretEnc) > 0,
		TLSMode: r.TlsMode, Sender: r.Sender, Enabled: r.Enabled, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func validateTransport(in TransportInput) (TransportInput, error) {
	in.Host = strings.TrimSpace(in.Host)
	in.Username = strings.TrimSpace(in.Username)
	in.Sender = strings.TrimSpace(in.Sender)
	if in.Kind != KindSMTP && in.Kind != KindSES {
		return in, invalid("kind must be smtp or ses")
	}
	if !validHost(in.Host) {
		return in, invalid("host must be a host name or an IP address, without scheme or port")
	}
	if in.Port < 1 || in.Port > 65535 {
		return in, invalid("port must be between 1 and 65535")
	}
	switch in.TLSMode {
	case TLSStartTLS, TLSImplicit, TLSNone:
	default:
		return in, invalid("tls_mode must be starttls, implicit or none")
	}
	if len(in.Username) > maxFieldLength || strings.ContainsAny(in.Username, "\r\n\x00") {
		return in, invalid("username is invalid")
	}
	if in.Username != "" && in.TLSMode == TLSNone {
		return in, invalid("a username needs TLS, credentials are not sent in plaintext")
	}
	if in.Sender != "" {
		a, err := policy.ParseAddress(in.Sender)
		if err != nil {
			return in, invalid("sender is not a valid address")
		}
		in.Sender = a.String()
	}
	if in.Kind == KindSES {
		switch {
		case in.TLSMode == TLSNone:
			return in, invalid("SES requires TLS")
		case in.Username == "":
			return in, invalid("SES requires the SMTP username")
		case in.Sender == "":
			return in, invalid("SES requires a verified sender address")
		}
	}
	return in, nil
}

func validHost(h string) bool {
	if _, err := netip.ParseAddr(h); err == nil {
		return true
	}
	return hostRE.MatchString(h) && !strings.Contains(h, "..")
}

// GetTransport returns the project's transport, ErrNotFound when none is set.
func (s *Service) GetTransport(ctx context.Context, scope authz.Scope) (Transport, error) {
	row, err := s.q.GetTransport(ctx, scope.ProjectID())
	if errors.Is(err, pgx.ErrNoRows) {
		return Transport{}, ErrNotFound
	}
	if err != nil {
		return Transport{}, fmt.Errorf("get transport: %w", err)
	}
	return toTransport(row), nil
}

// PutTransport creates or replaces the project's transport.
//
// Safety rules: a new transport starts disabled. Changing the connection
// (kind, host, port, username, TLS mode) disables it again and needs the
// password to be entered again, so a stored password can never be sent to a
// host the owner just typed in, and a changed relay is enabled on purpose.
func (s *Service) PutTransport(ctx context.Context, scope authz.Scope, in TransportInput) (Transport, error) {
	in, err := validateTransport(in)
	if err != nil {
		return Transport{}, err
	}
	var out Transport
	err = store.InTx(ctx, s.db, func(q *sqlcgen.Queries) error {
		existing, err := q.GetTransport(ctx, scope.ProjectID())
		exists := !errors.Is(err, pgx.ErrNoRows)
		if err != nil && exists {
			return err
		}
		connChanged := !exists || existing.Kind != in.Kind || existing.Host != in.Host ||
			int(existing.Port) != in.Port || existing.Username != in.Username || existing.TlsMode != in.TLSMode

		sealed, secretChanged, err := s.nextSecret(scope.ProjectID(), in, existing.SecretEnc, connChanged)
		if err != nil {
			return err
		}
		enabled := in.Enabled && exists && !connChanged
		row, err := q.UpsertTransport(ctx, sqlcgen.UpsertTransportParams{
			ProjectID: scope.ProjectID(), Kind: in.Kind, Host: in.Host, Port: int32(in.Port), //nolint:gosec // validated 1..65535
			Username: in.Username, SecretEnc: sealed, TlsMode: in.TLSMode, Sender: in.Sender, Enabled: enabled,
		})
		if err != nil {
			return err
		}
		out = toTransport(row)
		return audit.Record(ctx, q, audit.Entry{
			Action: "transport.update", ProjectID: scope.ProjectID(), Target: "transport",
			Detail: map[string]any{
				"kind": in.Kind, "host": in.Host, "port": in.Port, "tls_mode": in.TLSMode, "enabled": enabled,
				"has_username": in.Username != "", "secret_changed": secretChanged, "created": !exists,
			},
		})
	})
	if err != nil {
		if errors.Is(err, ErrInvalid) {
			return Transport{}, err
		}
		return Transport{}, fmt.Errorf("save transport: %w", err)
	}
	return out, nil
}

// nextSecret decides the stored password for an update. It returns the sealed
// blob (nil for none) and whether the password changed.
func (s *Service) nextSecret(projectID int64, in TransportInput, current []byte, connChanged bool) ([]byte, bool, error) {
	switch {
	case in.Secret != nil && *in.Secret != "":
		if in.Username == "" {
			return nil, false, invalid("a password needs a username")
		}
		blob, err := s.box.Seal([]byte(*in.Secret), secretContext(projectID))
		if err != nil {
			return nil, false, fmt.Errorf("seal secret: %w", err)
		}
		return blob, true, nil
	case in.Secret != nil: // explicitly cleared
		if in.Username != "" {
			return nil, false, invalid("a username needs a password")
		}
		return nil, len(current) > 0, nil
	case in.Username == "":
		return nil, len(current) > 0, nil
	case connChanged || len(current) == 0:
		return nil, false, invalid("enter the password again, the connection settings changed")
	}
	return current, false, nil
}

// DeleteTransport removes the transport (and with it the stored password).
func (s *Service) DeleteTransport(ctx context.Context, scope authz.Scope) error {
	err := store.InTx(ctx, s.db, func(q *sqlcgen.Queries) error {
		n, err := q.DeleteTransport(ctx, scope.ProjectID())
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		return audit.Record(ctx, q, audit.Entry{Action: "transport.delete", ProjectID: scope.ProjectID(), Target: "transport"})
	})
	if errors.Is(err, ErrNotFound) {
		return err
	}
	if err != nil {
		return fmt.Errorf("delete transport: %w", err)
	}
	return nil
}

// TestTransport opens a connection with the stored transport settings,
// secures it and authenticates, and sends no mail. It works whether or not the
// transport is enabled. It never tests unsaved settings, so the stored
// password only ever goes to the stored host. The error text carries nothing
// that the remote host said.
func (s *Service) TestTransport(ctx context.Context, scope authz.Scope) (CheckResult, error) {
	row, err := s.q.GetTransport(ctx, scope.ProjectID())
	if errors.Is(err, pgx.ErrNoRows) {
		return CheckResult{}, ErrNotFound
	}
	if err != nil {
		return CheckResult{}, fmt.Errorf("get transport: %w", err)
	}
	cfg, err := s.relayConfig(row)
	if err != nil {
		return CheckResult{Error: "the stored password can not be decrypted, enter it again"}, nil //nolint:nilerr // reported in the result
	}
	return s.relay.Check(ctx, cfg), nil
}

func (s *Service) relayConfig(row sqlcgen.Transport) (RelayConfig, error) {
	return relayConfigOf(s.box, row)
}

// Policy is the stored delivery policy of a project. A project without a row
// is reported as capture with Configured false.
type Policy struct {
	Mode            string    `json:"mode"`
	Allowlist       []string  `json:"allowlist"`
	RedirectAddress string    `json:"redirect_address"`
	SubjectPrefix   string    `json:"subject_prefix"`
	Configured      bool      `json:"configured"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// PolicyInput is a request to replace the policy.
type PolicyInput struct {
	Mode            string   `json:"mode"`
	Allowlist       []string `json:"allowlist"`
	RedirectAddress string   `json:"redirect_address"`
	SubjectPrefix   string   `json:"subject_prefix"`
}

func defaultPolicy() Policy {
	return Policy{Mode: string(policy.ModeCapture), Allowlist: []string{}}
}

func toPolicy(r sqlcgen.DeliveryPolicy) (Policy, error) {
	list := []string{}
	if err := json.Unmarshal(r.Allowlist, &list); err != nil {
		return Policy{}, fmt.Errorf("decode allowlist: %w", err)
	}
	return Policy{
		Mode: r.Mode, Allowlist: list, RedirectAddress: r.RedirectAddress, SubjectPrefix: r.SubjectPrefix,
		Configured: true, UpdatedAt: r.UpdatedAt,
	}, nil
}

// GetPolicy returns the project's policy, capture when none is stored.
func (s *Service) GetPolicy(ctx context.Context, scope authz.Scope) (Policy, error) {
	row, err := s.q.GetDeliveryPolicy(ctx, scope.ProjectID())
	if errors.Is(err, pgx.ErrNoRows) {
		return defaultPolicy(), nil
	}
	if err != nil {
		return Policy{}, fmt.Errorf("get policy: %w", err)
	}
	return toPolicy(row)
}

// PutPolicy validates and stores the policy.
func (s *Service) PutPolicy(ctx context.Context, scope authz.Scope, in PolicyInput) (Policy, error) {
	if !policy.Mode(in.Mode).Valid() {
		return Policy{}, invalid("mode must be capture, allow or redirect")
	}
	list, err := policy.NormalizeAllowlist(in.Allowlist)
	if err != nil {
		return Policy{}, invalid("%v", err)
	}
	redirect := strings.TrimSpace(in.RedirectAddress)
	if redirect != "" || in.Mode == string(policy.ModeRedirect) {
		a, perr := policy.ParseAddress(redirect)
		if perr != nil {
			return Policy{}, invalid("redirect_address must be a valid address (required in redirect mode)")
		}
		redirect = a.String()
	}
	if !policy.ValidSubjectPrefix(in.SubjectPrefix) {
		return Policy{}, invalid("subject_prefix is too long or has control characters")
	}
	raw, err := json.Marshal(list)
	if err != nil {
		return Policy{}, fmt.Errorf("encode allowlist: %w", err)
	}
	var out Policy
	err = store.InTx(ctx, s.db, func(q *sqlcgen.Queries) error {
		row, err := q.UpsertDeliveryPolicy(ctx, sqlcgen.UpsertDeliveryPolicyParams{
			ProjectID: scope.ProjectID(), Mode: in.Mode, Allowlist: raw, RedirectAddress: redirect, SubjectPrefix: in.SubjectPrefix,
		})
		if err != nil {
			return err
		}
		if out, err = toPolicy(row); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			Action: "policy.update", ProjectID: scope.ProjectID(), Target: "policy",
			Detail: map[string]any{"mode": in.Mode, "allowlist": list, "redirect_address": redirect},
		})
	})
	if err != nil {
		return Policy{}, fmt.Errorf("save policy: %w", err)
	}
	return out, nil
}
