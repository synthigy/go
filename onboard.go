package synthigy

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// OnboardUser is the created/reset account's identifier — enough to address
// it over Query/etc. Roles, groups, person_info, etc. are the caller's
// responsibility, not onboarding's; see IDENTITY_LIFECYCLE_PLAN.md P0.
type OnboardUser struct {
	XID string `json:"xid"`
}

// OnboardResult is the response from Onboard: a one-time account-claim link
// the caller delivers itself (email/SMS), its expiry (epoch ms), and the
// account's identifier.
type OnboardResult struct {
	OnboardURL string      `json:"onboard_url"`
	ExpiresAt  int64       `json:"expires_at"`
	User       OnboardUser `json:"user"`
}

// Onboard mints a one-time account-claim link (POST /oauth/onboard).
// Confidential client whose principal administers the account — RBAC update
// on User plus the row inside its owner-group write scope, which the shipped
// User Provisioner role grants — else PROVISION_FORBIDDEN. This Client's own
// client_credentials identity IS that principal, so no separate credential
// is needed here.
//
// xid addresses an EXISTING account — onboarding no longer creates accounts;
// create it first over Query/Sync (with person_info, roles, groups in one
// tree), then mint a ticket for it. A blank xid fails with XID_REQUIRED; one
// that doesn't resolve fails with USER_NOT_FOUND. OnboardReset(true)
// soft-recycles the account first (strips its identities, nulls its
// password, revokes its live sessions/tokens — leaves `active` untouched)
// before issuing a fresh link. OnboardMethods restricts which claim methods
// the page offers (e.g. OnboardMethods("password")); omitted allows every
// active federation provider plus password. OnboardReturnURL, if set, must
// match one of THIS client's registered redirections (or be a loopback URI);
// a successful DIRECT claim (browser) then redirects there instead of
// Synthigy's generic status page.
func (c *Client) Onboard(ctx context.Context, xid string, opts ...Opt) (*OnboardResult, error) {
	o := applyOpts(opts)
	body := map[string]any{"xid": xid}
	if o.onboardReset != nil {
		body["reset"] = *o.onboardReset
	}
	if o.onboardMethods != nil {
		body["methods"] = o.onboardMethods
	}
	if o.onboardTTLSeconds != nil {
		body["ttl_seconds"] = *o.onboardTTLSeconds
	}
	if o.onboardReturnURL != nil {
		body["return_url"] = *o.onboardReturnURL
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, newError("invalid onboard request: "+err.Error(), "INVALID_BODY")
	}

	resp, err := c.fetchAuth(ctx, http.MethodPost, c.endpoint+"/oauth/onboard", data,
		map[string]string{"Content-Type": "application/json"}, true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw := readBody(resp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, onboardError(raw, resp.StatusCode)
	}
	var out OnboardResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, newError("failed to decode onboard result: "+err.Error(), "INTERNAL_ERROR")
	}
	return &out, nil
}

// OnboardCompleteResult is the response from OnboardComplete: the redeemed
// account's identifier and its now-active state.
type OnboardCompleteResult struct {
	User   OnboardUser `json:"user"`
	Active bool        `json:"active"`
}

// OnboardComplete redeems an onboarding ticket without a browser
// (POST /oauth/onboard/complete) — the indirect face of the SAME ticket
// Onboard mints. Must be called by the SAME client that minted the ticket;
// any other client's bearer is rejected with CLAIM_INVALID, and the client
// must still administer the account (PROVISION_FORBIDDEN).
//
// The caller runs its own out-of-band proofing (email link, SMS OTP, push
// approval, KYC, a phone call — Synthigy never learns which) and, once
// satisfied, redeems the ticket itself instead of bouncing the user's
// browser through /oauth/claim. This never sets a credential — credentials
// are subject-only. The account activates with none; give it one via the
// claim page (password or a federated identity) or a later ticket.
func (c *Client) OnboardComplete(ctx context.Context, ticket string, opts ...Opt) (*OnboardCompleteResult, error) {
	body := map[string]any{"ticket": ticket}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, newError("invalid onboard-complete request: "+err.Error(), "INVALID_BODY")
	}

	resp, err := c.fetchAuth(ctx, http.MethodPost, c.endpoint+"/oauth/onboard/complete", data,
		map[string]string{"Content-Type": "application/json"}, true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw := readBody(resp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, onboardError(raw, resp.StatusCode)
	}
	var out OnboardCompleteResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, newError("failed to decode onboard-complete result: "+err.Error(), "INTERNAL_ERROR")
	}
	return &out, nil
}

// onboardError maps a non-2xx /oauth/onboard body to an *Error. The wire
// shape here is {"error": "<snake_case_code>"} — a bare string, unlike the
// {error:{code,message}} envelope /data uses — so it can't go through
// httpError/parseServerError; those expect an object and will silently miss
// the code on a type mismatch.
func onboardError(raw []byte, status int) *Error {
	var env struct {
		Error string `json:"error"`
	}
	if len(raw) > 0 && json.Unmarshal(raw, &env) == nil && env.Error != "" {
		code := strings.ToUpper(env.Error)
		e := newError("onboard failed: "+code, code)
		e.Status = status
		return e
	}
	e := newError("onboard request failed", "HTTP_ERROR")
	e.Status = status
	e.Details = string(raw)
	return e
}
