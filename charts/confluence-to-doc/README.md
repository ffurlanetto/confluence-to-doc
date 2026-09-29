# confluence-to-doc

Helm chart for [confluence-to-doc](../../README.md): export a Confluence page and its whole tree to Word
or PDF, asynchronously.

The chart deploys the application only. **PostgreSQL is external** — point `database` at a managed
instance — and so is object storage, the OIDC provider and Confluence.

## Install

```bash
kubectl create namespace confluence-to-doc

kubectl -n confluence-to-doc create secret generic c2d-database \
  --from-literal=DATABASE_URL='postgres://c2d:…@postgres.example.com:5432/c2d?sslmode=require'
kubectl -n confluence-to-doc create secret generic c2d-oidc \
  --from-literal=OIDC_CLIENT_SECRET='…'
kubectl -n confluence-to-doc create secret generic c2d-encryption \
  --from-literal=ENCRYPTION_KEY="$(openssl rand -base64 32)"

helm upgrade --install confluence-to-doc ./charts/confluence-to-doc \
  -n confluence-to-doc -f charts/confluence-to-doc/values-production.yaml
```

The image tag defaults to the chart's `appVersion`; see [docs/releasing.md](../../docs/releasing.md)
for the tags that are published and how a release is cut.

A bare `helm install` is refused on purpose: the chart checks up front that it has a public URL, a
database, an OIDC client, an encryption key and somewhere to put documents, and tells you which one is
missing rather than leaving pods to crash-loop.

## What it creates

| Object                    | Notes                                                                    |
| ------------------------- | ------------------------------------------------------------------------ |
| Deployment `-api`         | Serves the SPA and the REST API (`APP_ROLE=api`)                         |
| Deployment `-worker`      | Crawls Confluence and runs LibreOffice (`APP_ROLE=worker`)               |
| ConfigMap `-env`          | Every non-sensitive setting, injected with `envFrom`                     |
| Secret `-env`             | Only the secrets you asked the chart to manage; omitted when you bring your own |
| Service, Ingress          | For the API; the workers expose no API, only their health endpoints      |
| PersistentVolumeClaim     | Filesystem storage only                                                  |
| ServiceAccount, PDB, HPA  | Optional                                                                 |

Migrations run at application startup under an advisory lock, so there is no migration Job and several
replicas may start at once.

## Configuration

Everything the application reads is documented in [docs/configuration.md](../../docs/configuration.md);
the values below map onto it.

### Required

| Value                         | Description                                                    |
| ----------------------------- | -------------------------------------------------------------- |
| `publicUrl`                   | Public URL; sets the OIDC redirect URI and the origin check     |
| `confluence.baseUrl`          | Confluence Server / Data Center instance                        |
| `oidc.issuerUrl`, `oidc.clientId` | Confidential OIDC client                                    |
| `oidc.clientSecret` **or** `oidc.existingSecret` | Client secret                                 |
| `database.url` **or** `database.existingSecret`  | PostgreSQL DSN                               |
| `encryptionKey` **or** `existingEncryptionKeySecret` | 32 random bytes, base64                  |
| `storage.s3.bucket` (when `storage.type: s3`) | Bucket for generated documents              |

### Storage

`storage.type: s3` is the default and the one that fits Kubernetes: API and worker pods then share nothing.
Leave `storage.s3.accessKeyId` empty to authenticate through the pod's identity (IRSA, workload identity)
and put the annotation on `serviceAccount.annotations`.

`storage.type: filesystem` needs a **ReadWriteMany** volume as soon as workers run in their own pods; the
chart refuses the combination otherwise and suggests `worker.enabled: false`, which makes the API do the
work itself (`APP_ROLE=all`).

### Company Word template

A `.docx`/`.dotx` is binary, so it comes from a ConfigMap (`binaryData`) or a Secret you create yourself:

```bash
kubectl -n confluence-to-doc create configmap c2d-word-template \
  --from-file=template.dotx=./company-template.dotx
```

```yaml
wordTemplate:
  enabled: true
  existingConfigMap: c2d-word-template
  key: template.dotx
```

The file is validated at startup: a template the application cannot use stops the pod instead of producing
misformatted documents. See [docs/word-template.md](../../docs/word-template.md) — in particular, install
the corporate fonts in the image if you care about the PDF rendering.

### Telemetry

```yaml
telemetry:
  enabled: true
  endpoint: http://opentelemetry-collector.observability.svc:4317
  resourceAttributes:
    service.namespace: docs-platform
    team: platform
```

`resourceAttributes` is how you tag the owning application and team; it lands on every metric and span.

## Operational notes

- **Rolling updates of workers**: an export in flight is given 20 s to finish before the job returns to the
  queue, so `worker.terminationGracePeriodSeconds` defaults to 60 and the deployment replaces one pod at a
  time. Shorten the grace period and you turn clean handovers into retries.
- **Memory**: LibreOffice is the hungry part. 1 GiB per worker with the default
  `export.workerConcurrency: 2` is a reasonable start; watch `c2d.export.duration` and OOM kills before
  lowering it.
- **Read-only root filesystem** is the default. LibreOffice only writes under `/tmp` and `$HOME`, both
  mounted as `emptyDir`; `podSecurityContext.fsGroup` makes them writable by the non-root user.
- **Changing `ENCRYPTION_KEY`** makes every stored Confluence token unreadable, and every user has to enter
  theirs again.
- Pod annotations carry a checksum of the ConfigMap and Secret, so a configuration change rolls the pods.
