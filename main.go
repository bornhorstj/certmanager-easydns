// =============================================================================
// cert-manager DNS-01 Webhook for easyDNS
// =============================================================================
//
// WHAT THIS FILE DOES:
//   This is the "brain" of the webhook. It runs as a small web server inside
//   your Kubernetes cluster. When cert-manager needs to prove you own a domain
//   (to get a TLS certificate), it calls this webhook, which then:
//
//     1. Talks to the easyDNS REST API to CREATE a special DNS TXT record
//        (Let's Encrypt reads this record to verify domain ownership)
//     2. After the certificate is issued, talks to easyDNS again to DELETE
//        that temporary TXT record (cleanup)
//
// HOW ACME DNS-01 WORKS IN PLAIN ENGLISH:
//   - You want a cert for "mysite.com"
//   - Let's Encrypt says: "Prove you own it by putting a specific value in
//     a DNS TXT record at _acme-challenge.mysite.com"
//   - This webhook creates that TXT record via the easyDNS API
//   - Let's Encrypt checks the DNS record and issues the cert
//   - This webhook deletes the temporary TXT record
//
// EASYDNS API ENDPOINTS USED:
//   Add TXT record:    PUT  /zones/records/add/{zone}/TXT?format=json
//   List TXT records:  GET  /zones/records/all/{zone}?format=json&type=TXT
//   Delete TXT record: DELETE /zones/records/{zone}/{record_id}?format=json
//   Authentication:    HTTP Basic Auth (token:key as username:password)
//
// =============================================================================

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	// cert-manager webhook SDK — provides the framework we plug into
	"github.com/cert-manager/cert-manager/pkg/acme/webhook/apis/acme/v1alpha1"
	"github.com/cert-manager/cert-manager/pkg/acme/webhook/cmd"

	// Kubernetes client libraries — used to read our API credentials from Secrets
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// =============================================================================
// CONFIGURATION
// These constants identify this webhook to cert-manager.
// groupName must be a domain you own — it's just a unique namespace identifier,
// not an actual DNS lookup.
// =============================================================================

const (
	// groupName uniquely identifies this webhook provider.
	// Must match the groupName in your ClusterIssuer YAML.
	groupName = "acme.easydns.com"

	// solverName is the short name used in the ClusterIssuer solver config.
	// Must match solverName in your ClusterIssuer YAML.
	solverName = "easydns"
)

// =============================================================================
// DATA STRUCTURES
// These structs represent JSON responses from the easyDNS REST API.
// =============================================================================

// easyDNSConfig holds the settings cert-manager passes to our webhook.
// These values come from the `config:` block in your ClusterIssuer YAML.
type easyDNSConfig struct {
	// APIEndpoint is the easyDNS REST API base URL.
	// Sandbox: https://sandbox.rest.easydns.net:3001
	// Production: https://rest.easydns.net
	APIEndpoint string `json:"apiEndpoint"`

	// Zone overrides the DNS zone used in EasyDNS API calls.
	// Use this when the zone cert-manager resolves (e.g. "8bit.dark-byte.io")
	// does not match the zone EasyDNS manages (e.g. "dark-byte.io").
	// If empty, ch.ResolvedZone is used.
	Zone string `json:"zone"`

	// APITokenSecretRef points to the Kubernetes Secret holding the API token.
	APITokenSecretRef secretKeySelector `json:"apiTokenSecretRef"`

	// APIKeySecretRef points to the Kubernetes Secret holding the API key.
	APIKeySecretRef secretKeySelector `json:"apiKeySecretRef"`

	// TTL is how long (in seconds) the temporary TXT record should live in DNS.
	// 300 seconds (5 minutes) is a good default — short enough to not linger,
	// long enough to propagate.
	TTL int `json:"ttl"`
}

// secretKeySelector tells us where to find a value in a Kubernetes Secret.
// e.g., "look in secret named 'easydns-credentials', key 'api-token'"
type secretKeySelector struct {
	Name      string `json:"name"` // Name of the Kubernetes Secret
	Namespace string `json:"namespace"` // Namespace where the Secret lives
	Key       string `json:"key"`  // Which key inside the Secret to use
}

// easyDNSRecord represents a single DNS record returned by the easyDNS API.
// We use this when listing records to find the ID of a record we want to delete.
type easyDNSRecord struct {
	ID    string `json:"id"`    // Unique record ID (needed for deletion)
	Host  string `json:"host"`  // Subdomain part (e.g., "_acme-challenge")
	Type  string `json:"type"`  // Record type (we care about "TXT")
	RData string `json:"rdata"` // The record value (the ACME challenge token)
}

// easyDNSListResponse wraps the list of records returned by the API.
type easyDNSListResponse struct {
	Data []easyDNSRecord `json:"data"`
}

// =============================================================================
// WEBHOOK SOLVER
// This struct implements the cert-manager webhook interface.
// cert-manager will call Present() to add a DNS record and CleanUp() to remove it.
// =============================================================================

// easyDNSSolver is our webhook implementation.
// It holds a Kubernetes client so we can read Secrets to get API credentials.
type easyDNSSolver struct {
	k8sClient kubernetes.Interface
}

// Name returns the solver name. Must match `solverName` in the ClusterIssuer.
func (s *easyDNSSolver) Name() string {
	return solverName
}

// Initialize is called once when the webhook starts up.
// We set up our Kubernetes client here so we can read Secrets later.
func (s *easyDNSSolver) Initialize(kubeClientConfig *rest.Config, stopCh <-chan struct{}) error {
	client, err := kubernetes.NewForConfig(kubeClientConfig)
	if err != nil {
		return fmt.Errorf("failed to create Kubernetes client: %w", err)
	}
	s.k8sClient = client
	return nil
}

// Present is called by cert-manager when it needs to CREATE the ACME challenge TXT record.
// Parameters:
//   - ch.ResolvedFQDN: the full DNS name to create (e.g., "_acme-challenge.mysite.com.")
//   - ch.ResolvedZone: the DNS zone (e.g., "mysite.com.")
//   - ch.Key: the challenge token value to put in the TXT record
func (s *easyDNSSolver) Present(ch *v1alpha1.ChallengeRequest) error {
	// Step 1: Load our configuration from the ClusterIssuer
	cfg, err := s.loadConfig(ch)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Step 2: Get our API credentials from the Kubernetes Secret
	token, key, err := s.getCredentials(cfg, ch.ResourceNamespace)
	if err != nil {
		return fmt.Errorf("failed to get credentials: %w", err)
	}

	// Step 3: Figure out which zone and hostname to use
	// ch.ResolvedFQDN might be "_acme-challenge.k8s.8bit.dark-byte.io."
	// ch.ResolvedZone might be "8bit.dark-byte.io." but EasyDNS may manage
	// the parent zone "dark-byte.io" — use cfg.Zone to override if needed.
	zone := strings.TrimSuffix(ch.ResolvedZone, ".")
	if cfg.Zone != "" {
		zone = cfg.Zone
	}
	fqdn := strings.TrimSuffix(ch.ResolvedFQDN, ".")
	host := strings.TrimSuffix(fqdn, "."+zone)

	// Step 4: Build the easyDNS API request to add the TXT record
	// API endpoint: PUT /zones/records/add/{zone}/TXT?format=json
	url := fmt.Sprintf("%s/zones/records/add/%s/TXT?format=json", cfg.APIEndpoint, zone)

	ttl := cfg.TTL
	if ttl == 0 {
		ttl = 300 // Default: 5 minutes
	}

	// The JSON body easyDNS expects
	body := map[string]interface{}{
		"host":  host,
		"rdata": ch.Key, // This is the ACME challenge token value
		"ttl":   ttl,
		"type":  "TXT",
	}

	// Step 5: Make the API call to easyDNS
	if err := s.apiRequest("PUT", url, body, token, key, nil); err != nil {
		return fmt.Errorf("failed to create TXT record for %s: %w", fqdn, err)
	}

	fmt.Printf("✅ Created TXT record: %s = %s\n", fqdn, ch.Key)
	return nil
}

// CleanUp is called by cert-manager AFTER the certificate is issued.
// It removes the temporary TXT record we created in Present().
func (s *easyDNSSolver) CleanUp(ch *v1alpha1.ChallengeRequest) error {
	// Step 1: Load config and credentials (same as Present)
	cfg, err := s.loadConfig(ch)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	token, key, err := s.getCredentials(cfg, ch.ResourceNamespace)
	if err != nil {
		return fmt.Errorf("failed to get credentials: %w", err)
	}

	zone := strings.TrimSuffix(ch.ResolvedZone, ".")
	if cfg.Zone != "" {
		zone = cfg.Zone
	}
	fqdn := strings.TrimSuffix(ch.ResolvedFQDN, ".")
	host := strings.TrimSuffix(fqdn, "."+zone)

	// Step 2: List all TXT records in the zone to find the one we need to delete.
	// We need the record's ID to delete it — easyDNS requires it.
	// API endpoint: GET /zones/records/all/{zone}?format=json&type=TXT
	listURL := fmt.Sprintf("%s/zones/records/all/%s?format=json&type=TXT", cfg.APIEndpoint, zone)

	var listResp easyDNSListResponse
	if err := s.apiRequest("GET", listURL, nil, token, key, &listResp); err != nil {
		return fmt.Errorf("failed to list TXT records for zone %s: %w", zone, err)
	}

	// Step 3: Find the specific record that matches our host + challenge token
	var recordID string
	for _, record := range listResp.Data {
		if record.Host == host && record.RData == ch.Key {
			recordID = record.ID
			break
		}
	}

	if recordID == "" {
		// Record not found — it may have already been deleted. That's okay.
		fmt.Printf("⚠️  TXT record for %s not found (may already be deleted)\n", fqdn)
		return nil
	}

	// Step 4: Delete the record using its ID
	// API endpoint: DELETE /zones/records/{zone}/{record_id}?format=json
	deleteURL := fmt.Sprintf("%s/zones/records/%s/%s?format=json", cfg.APIEndpoint, zone, recordID)

	if err := s.apiRequest("DELETE", deleteURL, nil, token, key, nil); err != nil {
		return fmt.Errorf("failed to delete TXT record %s: %w", recordID, err)
	}

	fmt.Printf("🗑️  Deleted TXT record: %s (id=%s)\n", fqdn, recordID)
	return nil
}

// =============================================================================
// HELPER FUNCTIONS
// =============================================================================

// loadConfig reads the solver configuration from the ChallengeRequest.
// cert-manager passes the `config:` block from the ClusterIssuer to us as JSON.
func (s *easyDNSSolver) loadConfig(ch *v1alpha1.ChallengeRequest) (*easyDNSConfig, error) {
	cfg := &easyDNSConfig{}

	if ch.Config == nil {
		return nil, fmt.Errorf("no config provided in ClusterIssuer — check your YAML")
	}

	if err := json.Unmarshal(ch.Config.Raw, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config JSON: %w", err)
	}

	if cfg.APIEndpoint == "" {
		return nil, fmt.Errorf("apiEndpoint is required in the ClusterIssuer config")
	}

	return cfg, nil
}

// getCredentials fetches the easyDNS API token and key from a Kubernetes Secret.
// Kubernetes Secrets are base64-encoded; the client library decodes them for us.
func (s *easyDNSSolver) getCredentials(cfg *easyDNSConfig, defaultNamespace string) (token, key string, err error) {
	// Figure out which namespace to look in
	tokenNS := cfg.APITokenSecretRef.Namespace
	if tokenNS == "" {
		tokenNS = defaultNamespace
	}
	keyNS := cfg.APIKeySecretRef.Namespace
	if keyNS == "" {
		keyNS = defaultNamespace
	}

	// Fetch the Secret containing the API token
	tokenSecret, err := s.k8sClient.CoreV1().Secrets(tokenNS).Get(
		context.Background(),
		cfg.APITokenSecretRef.Name,
		metav1.GetOptions{},
	)
	if err != nil {
		return "", "", fmt.Errorf("could not find secret '%s' in namespace '%s': %w",
			cfg.APITokenSecretRef.Name, tokenNS, err)
	}

	// Fetch the Secret containing the API key
	keySecret, err := s.k8sClient.CoreV1().Secrets(keyNS).Get(
		context.Background(),
		cfg.APIKeySecretRef.Name,
		metav1.GetOptions{},
	)
	if err != nil {
		return "", "", fmt.Errorf("could not find secret '%s' in namespace '%s': %w",
			cfg.APIKeySecretRef.Name, keyNS, err)
	}

	// Extract the values from the secrets
	tokenBytes, ok := tokenSecret.Data[cfg.APITokenSecretRef.Key]
	if !ok {
		return "", "", fmt.Errorf("key '%s' not found in secret '%s'",
			cfg.APITokenSecretRef.Key, cfg.APITokenSecretRef.Name)
	}

	keyBytes, ok := keySecret.Data[cfg.APIKeySecretRef.Key]
	if !ok {
		return "", "", fmt.Errorf("key '%s' not found in secret '%s'",
			cfg.APIKeySecretRef.Key, cfg.APIKeySecretRef.Name)
	}

	return string(tokenBytes), string(keyBytes), nil
}

// apiRequest makes an HTTP request to the easyDNS API.
// easyDNS uses HTTP Basic Auth with your API token as the username
// and API key as the password.
func (s *easyDNSSolver) apiRequest(method, url string, body interface{}, token, key string, result interface{}) error {
	// Encode the request body as JSON (if there is one)
	var bodyReader io.Reader
	if body != nil {
		bodyBytes, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to encode request body: %w", err)
		}
		bodyReader = bytes.NewReader(bodyBytes)
	}

	// Create the HTTP request
	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return fmt.Errorf("failed to create HTTP request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	// easyDNS uses HTTP Basic Auth: token = username, key = password
	req.SetBasicAuth(token, key)

	// Make the HTTP call
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP request to easyDNS failed: %w", err)
	}
	defer resp.Body.Close()

	// Read the response body
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}

	// Check for API errors (anything that's not 2xx is a problem)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("easyDNS API error (HTTP %d): %s", resp.StatusCode, string(respBytes))
	}

	// Parse the response into the result struct (if one was provided)
	if result != nil && len(respBytes) > 0 {
		if err := json.Unmarshal(respBytes, result); err != nil {
			return fmt.Errorf("failed to parse API response: %w", err)
		}
	}

	return nil
}

// =============================================================================
// ENTRY POINT
// =============================================================================

func main() {
	// Print startup message so you can see it in kubectl logs
	fmt.Printf("🚀 Starting cert-manager easyDNS webhook (solver: %s, group: %s)\n",
		solverName, groupName)

	// The cert-manager webhook SDK takes care of all the TLS, HTTP server,
	// and Kubernetes API registration. We just hand it our solver.
	cmd.RunWebhookServer(groupName,
		&easyDNSSolver{},
	)

	// This line is only reached on shutdown
	fmt.Println("👋 easyDNS webhook stopped")
	os.Exit(0)
}
