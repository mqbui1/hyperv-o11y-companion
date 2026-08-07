// Package metadata writes the guest_os custom property onto the Splunk
// Observability Cloud `vm` dimension via the SignalFx metadata API
// (GET -> merge -> PUT, since the API has overwrite semantics) — same
// pattern as enrich-vm-guest-os.ps1, ported to Go so scvmm-poller can do
// this on its own schedule without a second process.
package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

type Client struct {
	Realm      string
	AccessToken string
	HTTPClient *http.Client
}

func New(realm, accessToken string) *Client {
	return &Client{Realm: realm, AccessToken: accessToken, HTTPClient: &http.Client{Timeout: 15 * time.Second}}
}

type dimensionDoc struct {
	CustomProperties map[string]string `json:"customProperties"`
	Tags             []string          `json:"tags"`
}

// SetGuestOS performs GET -> merge -> PUT for the vm dimension named by
// vmName, setting guest_os/guest_os_detail/guest_os_source. Returns
// (changed=false, nil) if the merged doc is identical to what's already
// stored, matching the "unchanged" bookkeeping in enrich-vm-guest-os.ps1.
func (c *Client) SetGuestOS(ctx context.Context, vmName, guestOS, detail, source string) (changed bool, err error) {
	base := fmt.Sprintf("https://api.%s.signalfx.com/v2/dimension/vm/%s", c.Realm, url.PathEscape(vmName))

	doc, err := c.get(ctx, base)
	if err != nil {
		return false, err
	}
	if doc.CustomProperties == nil {
		doc.CustomProperties = map[string]string{}
	}
	if doc.CustomProperties["guest_os"] == guestOS &&
		doc.CustomProperties["guest_os_detail"] == detail &&
		doc.CustomProperties["guest_os_source"] == source {
		return false, nil
	}
	doc.CustomProperties["guest_os"] = guestOS
	doc.CustomProperties["guest_os_detail"] = detail
	doc.CustomProperties["guest_os_source"] = source

	body, err := json.Marshal(doc)
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, base, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-SF-Token", c.AccessToken)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("PUT %s: %s: %s", base, resp.Status, string(b))
	}
	return true, nil
}

func (c *Client) get(ctx context.Context, url string) (dimensionDoc, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return dimensionDoc{}, err
	}
	req.Header.Set("X-SF-Token", c.AccessToken)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return dimensionDoc{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return dimensionDoc{}, nil // not seen yet - start fresh, matches script's 404 handling
	}
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return dimensionDoc{}, fmt.Errorf("GET %s: %s: %s", url, resp.Status, string(b))
	}
	var doc dimensionDoc
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return dimensionDoc{}, err
	}
	return doc, nil
}
