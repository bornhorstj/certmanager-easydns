# cert-manager easyDNS Webhook

Automatically issue and renew TLS certificates (including wildcards) using
**cert-manager** + **Let's Encrypt** + **easyDNS DNS-01 challenge**.

---

## How It All Fits Together

```
┌─────────────────────────────────────────────────────────────────┐
│                     Your Kubernetes Cluster                      │
│                                                                  │
│  cert-manager                                                    │
│  ┌─────────────┐   "Prove you own        ┌──────────────────┐   │
│  │ Certificate │   yourdomain.com"       │  easyDNS Webhook  │   │
│  │  Resource   │ ──────────────────────► │  (this project)   │   │
│  └─────────────┘                         └────────┬─────────┘   │
│                                                   │              │
└───────────────────────────────────────────────────┼─────────────┘
                                                    │ PUT /zones/records/add
                                                    ▼
                                          ┌──────────────────────┐
                                          │   easyDNS REST API   │
                                          │  (adds TXT record)   │
                                          └──────────────────────┘
                                                    │
                              Let's Encrypt reads ◄─┘
                              _acme-challenge TXT
                              record, then issues
                              your certificate ✅
```

**Plain English:**
1. You create a `Certificate` resource in Kubernetes
2. cert-manager asks Let's Encrypt for a cert
3. Let's Encrypt says "prove you own the domain by creating a special DNS TXT record"
4. cert-manager calls **this webhook**, which talks to the easyDNS API to create that TXT record
5. Let's Encrypt verifies the TXT record and issues the certificate
6. The webhook cleans up the temporary TXT record
7. Your cert is stored in a Kubernetes Secret, ready to use

---

## Prerequisites

- Kubernetes cluster (v1.21+)
- [cert-manager](https://cert-manager.io/docs/installation/) installed (`>= v1.0`)
- easyDNS account with API access (token + key)
- Docker (to build the webhook image)
- `kubectl` and optionally `helm`

---

## Project Structure

```
cert-manager-webhook-easydns/
│
├── main.go                          # The webhook Go server (core logic)
├── go.mod                           # Go dependencies
├── Dockerfile                       # Builds the webhook container image
│
├── kubernetes/                      # Plain YAML files (no Helm required)
│   ├── 01-secret.yaml               # Your easyDNS API credentials
│   ├── 02-clusterissuer.yaml        # How cert-manager gets certs (staging + prod)
│   └── 03-certificate.yaml         # Request an actual TLS cert for your domain
│
└── deploy/helm/                     # Helm chart (deploys the webhook itself)
    └── cert-manager-webhook-easydns/
        ├── Chart.yaml
        ├── values.yaml              # ← Edit this before deploying
        └── templates/
            ├── deployment.yaml      # Runs the webhook pod
            ├── service.yaml         # Exposes the webhook inside the cluster
            ├── certificate.yaml     # TLS cert for the webhook server itself
            ├── rbac.yaml            # Kubernetes permissions
            └── _helpers.tpl         # Shared template snippets
```

---

## Step-by-Step Deployment Guide

### Step 1 — Build and Push the Webhook Image

```bash
# Replace "your-registry" with your Docker Hub username, ECR, GCR, etc.
docker build -t your-registry/cert-manager-webhook-easydns:1.0.0 .
docker push your-registry/cert-manager-webhook-easydns:1.0.0
```

### Step 2 — Edit the Helm Values

Open `deploy/helm/cert-manager-webhook-easydns/values.yaml` and update:

```yaml
image:
  repository: your-registry/cert-manager-webhook-easydns  # ← Your registry
  tag: "1.0.0"
```

### Step 3 — Deploy the Webhook with Helm

```bash
helm install cert-manager-webhook-easydns \
  ./deploy/helm/cert-manager-webhook-easydns \
  --namespace cert-manager \
  --create-namespace
```

Verify it's running:
```bash
kubectl get pods -n cert-manager | grep easydns
# Should show: cert-manager-webhook-easydns-... Running
```

### Step 4 — Add Your easyDNS Credentials

```bash
kubectl create secret generic easydns-credentials \
  --namespace cert-manager \
  --from-literal=api-token='YOUR_EASYDNS_TOKEN' \
  --from-literal=api-key='YOUR_EASYDNS_KEY'
```

Or apply the file (after editing it):
```bash
# Edit kubernetes/01-secret.yaml with your real credentials first!
kubectl apply -f kubernetes/01-secret.yaml
```

### Step 5 — Deploy the ClusterIssuers (Staging First!)

```bash
# Edit kubernetes/02-clusterissuer.yaml
# Replace "admin@yourdomain.com" with your real email

kubectl apply -f kubernetes/02-clusterissuer.yaml

# Verify both issuers show "Ready: True"
kubectl describe clusterissuer letsencrypt-staging-easydns
kubectl describe clusterissuer letsencrypt-prod-easydns
```

### Step 6 — Request a Test Certificate (Staging)

```bash
# Edit kubernetes/03-certificate.yaml
# Replace "yourdomain.com" with your actual domain
# Make sure issuerRef.name is "letsencrypt-staging-easydns"

kubectl apply -f kubernetes/03-certificate.yaml

# Watch the certificate being issued (may take 1-2 minutes)
kubectl describe certificate yourdomain-tls -n default

# Check the ACME challenge status if something seems stuck
kubectl get challenges -A
kubectl get orders -A
```

When staging works, the Certificate will show `Ready: True`.
The cert won't be browser-trusted (it's staging), but it proves everything works.

### Step 7 — Switch to Production

Edit `kubernetes/03-certificate.yaml`:
```yaml
issuerRef:
  name: letsencrypt-prod-easydns   # ← Change this
```

Also update `kubernetes/02-clusterissuer.yaml` production issuer:
```yaml
apiEndpoint: "https://rest.easydns.net"   # ← Remove sandbox URL
```

```bash
kubectl apply -f kubernetes/02-clusterissuer.yaml
kubectl apply -f kubernetes/03-certificate.yaml
```

---

## Troubleshooting

### Check webhook logs
```bash
kubectl logs -n cert-manager deploy/cert-manager-webhook-easydns-cert-manager-webhook-easydns
```

### Certificate stuck in "pending"
```bash
kubectl get challenges -A
kubectl describe challenge <challenge-name> -n default
```

### Common issues

| Problem | Likely Cause | Fix |
|---------|-------------|-----|
| `Failed to call webhook` | Webhook pod not running | Check `kubectl get pods -n cert-manager` |
| `Could not find secret` | Wrong secret name/namespace | Check `01-secret.yaml` matches ClusterIssuer config |
| `easyDNS API error (HTTP 401)` | Wrong credentials | Re-check your API token and key |
| `easyDNS API error (HTTP 400)` | Wrong zone name | Verify domain is in your easyDNS account |
| Certificate not Ready after 5 min | DNS propagation delay | Wait longer, check `kubectl get challenges -A` |
| `rate limit` error | Hit Let's Encrypt limits | You're probably on staging — use production issuer |

### easyDNS Sandbox Note

The sandbox (`sandbox.rest.easydns.net:3001`) accepts API calls and creates
records, but those records are NOT publicly resolvable. This means staging
certificate issuance will fail at the DNS verification step.

**The sandbox is for testing your API credentials and webhook connectivity only.**
To actually get a certificate (even a staging one), use the production easyDNS API:
```yaml
apiEndpoint: "https://rest.easydns.net"
```

---

## easyDNS API Reference

| Action | Method | Endpoint |
|--------|--------|----------|
| Add TXT record | `PUT` | `/zones/records/add/{zone}/TXT?format=json` |
| List records | `GET` | `/zones/records/all/{zone}?format=json&type=TXT` |
| Delete record | `DELETE` | `/zones/records/{zone}/{record_id}?format=json` |
| Auth | Basic Auth | `token:key` as username:password |
