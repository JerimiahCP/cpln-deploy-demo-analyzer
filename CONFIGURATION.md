# Configuration

Configuration is managed entirely through **GitHub Actions variables and secrets**.
The deploy workflow injects values into the CPLN YAML templates via `envsubst` before applying.

For local development, copy `.env.example` to `.env`.

---

## GitHub Setup

Go to your repo → **Settings → Secrets and variables → Actions**

### Secrets (sensitive)

| Secret | Description | How to get it |
|---|---|---|
| `CPLN_TOKEN` | Control Plane service account token | Console → Org → Service Accounts → create one, generate a key |

### Variables (non-sensitive)

| Variable | Description | Example |
|---|---|---|
| `CPLN_ORG` | Your Control Plane org name | `cpln-customer-demos` |
| `AWS_REGION` | AWS region the S3 bucket is in | `us-east-1` |
| `AWS_CLOUD_ACCOUNT` | Control Plane cloud account resource name | `aws` |

---

## What the analyzer needs at runtime

The analyzer has no static AWS credentials. Control Plane injects temporary STS credentials
at runtime via `analyzer-identity`, which grants `AmazonS3ReadOnlyAccess` — read-only,
nothing else.

The `AWS_REGION` variable is the only runtime config the container needs. Everything else
comes from the workload identity.

---

## Local development

Copy `.env.example` to `.env`, fill in real AWS credentials for direct S3 access.

```bash
cp .env.example .env
go run .
```

Test with curl:
```bash
curl -X POST http://localhost:8081/analyze \
  -H 'Content-Type: application/json' \
  -d '{"bucket":"your-bucket","key":"staging/files/abc123/data.csv"}'
```
