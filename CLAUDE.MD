# cert-manager easyDNS Webhook

A Go-based Kubernetes webhook that enables cert-manager to issue TLS certificates
(including wildcards) via the easyDNS DNS-01 ACME challenge.

## What This Project Does

When cert-manager needs to prove domain ownership for a TLS certificate, it calls
this webhook. The webhook talks to the easyDNS REST API to create a temporary DNS
TXT record (`_acme-challenge.<domain>`), Let's Encrypt verifies it, the cert is
issued, and the webhook cleans up the TXT record.

## Project Structure

```
cert-manager-webhook-easydns/
├── main.go                          # Core webhook logic — start here
├── go.mod                           # Go module and dependencies
├── Dockerfile                       # Two-stage build (compile → minimal Alpine image)
├── kubernetes/
│   ├── 01-secret.yaml               # easyDNS API credentials (never commit real values)
│   ├── 02-clusterissuer.yaml        # cert-manager issuers (staging + production)
│   └── 03-certificate.yaml         # Certificate request resource
└── deploy/helm/cert-manager-webhook-easydns/
    ├── values.yaml                  # Helm config — the only file users need to edit
    ├── Chart.yaml
    └── templates/
        ├── deployment.yaml          # Webhook pod
        ├── service.yaml             # Internal cluster service + APIService registration
        ├── certificate.yaml        # TLS cert for the webhook server itself
        ├── rbac.yaml                # ServiceAccount + minimal RBAC permissions
        └── _helpers.tpl             # Shared template helpers
```

## Key Architecture Decisions

- **groupName** (`acme.easydns.com`) and **solverName** (`easydns`) are constants in
  `main.go` and must match exactly in `values.yaml` and the ClusterIssuer YAML config blocks.
- The webhook reads easyDNS credentials from a Kubernetes Secret (never hardcoded).
  Secret name/namespace/key are configured via the ClusterIssuer's `config:` block.
- easyDNS auth is HTTP Basic Auth: `token` as username, `key` as password.
- The webhook implements three cert-manager interface methods: `Name()`, `Present()`, `CleanUp()`.

## easyDNS API Endpoints

| Action | Method | Path |
|--------|--------|------|
| Add TXT record | `PUT` | `/zones/records/add/{zone}/TXT?format=json` |
| List TXT records | `GET` | `/zones/records/all/{zone}?format=json&type=TXT` |
| Delete record | `DELETE` | `/zones/records/{zone}/{record_id}?format=json` |

Sandbox base URL: `https://sandbox.rest.easydns.net:3001`
Production base URL: `https://rest.easydns.net`

> ⚠️ Sandbox records are NOT publicly resolvable — use sandbox for API connectivity
> testing only. You need the production API to actually complete ACME challenges.

## Commands

```bash
# Build and run locally
go build -o webhook .
go test ./...

# Build container image
docker build -t your-registry/cert-manager-webhook-easydns:latest .
docker push your-registry/cert-manager-webhook-easydns:latest

# Deploy webhook via Helm
helm install cert-manager-webhook-easydns \
  ./deploy/helm/cert-manager-webhook-easydns \
  --namespace cert-manager

# Apply Kubernetes resources (in order)
kubectl apply -f kubernetes/01-secret.yaml
kubectl apply -f kubernetes/02-clusterissuer.yaml
kubectl apply -f kubernetes/03-certificate.yaml

# Check status
kubectl describe clusterissuer letsencrypt-staging-easydns
kubectl describe certificate yourdomain-tls -n default
kubectl get challenges -A
kubectl logs -n cert-manager deploy/cert-manager-webhook-easydns-cert-manager-webhook-easydns
```

## Code Style

- Go standard formatting (`gofmt`)
- All exported types and functions must have doc comments
- Error messages should be lowercase and wrapped with `%w` for chain context
- Log lines use emoji prefixes: `✅` success, `⚠️` warning, `🚀` startup, `🗑️` deletion

## Important Constraints

- **Never commit real credentials** to `01-secret.yaml` — use placeholder text
- The `groupName` constant in `main.go`, `values.yaml`, and `02-clusterissuer.yaml`
  must all be identical or the webhook will not be called
- cert-manager must be installed **before** deploying this webhook
- Helm chart deploys the webhook pod; `kubernetes/` files configure cert issuance —
  these are separate concerns

## Testing Workflow

1. Deploy webhook and verify pod is `Running`
2. Apply staging ClusterIssuer (points to `sandbox.rest.easydns.net:3001`)
   — verifies webhook connectivity and credential loading
3. Switch to production easyDNS endpoint (`rest.easydns.net`) for real cert issuance
4. Apply staging Certificate — verifies end-to-end DNS-01 flow with Let's Encrypt staging
5. Switch Certificate to production ClusterIssuer for browser-trusted cert
