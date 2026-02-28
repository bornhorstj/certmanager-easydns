module github.com/youorg/cert-manager-webhook-easydns

// Update "youorg" to your GitHub org/username before publishing.
// This module path is used for Go imports — it doesn't need to be a real URL
// during development, but should match your actual repo when you publish.

go 1.21

require (
	// cert-manager webhook SDK — provides the framework our solver plugs into
	github.com/cert-manager/cert-manager v1.14.4

	// Kubernetes client — used to read Secrets containing our API credentials
	k8s.io/client-go v0.29.3
	k8s.io/apimachinery v0.29.3
	k8s.io/api v0.29.3
)
