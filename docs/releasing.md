# Releasing

The container image is published to the GitHub Container Registry by
[`.github/workflows/release.yml`](../.github/workflows/release.yml).

| Trigger          | Tags published                                        |
| ---------------- | ----------------------------------------------------- |
| push to `main`   | `edge`, `sha-<commit>`                                 |
| tag `v1.2.3`     | `1.2.3`, `1.2`, `latest`, `sha-<commit>`               |
| manual dispatch  | the same tags the current ref would produce            |

```
ghcr.io/ffurlanetto/confluence-to-doc:1.2.3
```

`edge` is whatever is on `main`; it is a moving target and not meant for anything but trying the latest
work. Deployments should pin a version tag, or a digest.

## Cutting a release

1. Make sure `main` is green: the release workflow builds and pushes, it does not re-run the test suite.
2. Update `appVersion` in [`charts/confluence-to-doc/Chart.yaml`](../charts/confluence-to-doc/Chart.yaml)
   to the version being released, and bump the chart's own `version`. The chart defaults its image tag to
   `appVersion`, so the two stay in step.
3. Tag and push:

   ```bash
   git tag -a v1.2.3 -m "v1.2.3"
   git push origin v1.2.3
   ```

4. Watch the run. It finishes with the published tags and the manifest digest in the job summary.

Only `vMAJOR.MINOR.PATCH` tags publish a release; anything else is ignored. A pre-release suffix
(`v1.2.3-rc.1`) is published under its exact version and does **not** move `latest`.

## What the workflow does

- Builds `linux/amd64` and `linux/arm64` **on their own native runners**. Emulating arm64 through QEMU
  would take tens of minutes for an image carrying LibreOffice; the repository is public, so the free
  `ubuntu-24.04-arm` runners are available.
- Each architecture is pushed **by digest only**, so no single-architecture tag is ever visible. The two
  are then joined into one multi-platform manifest that carries the tags.
- The manifest is read back and the job fails unless it really contains both platforms — a partial join
  would otherwise publish an image that silently does not run on half the fleet.
- `VERSION` is passed as a build argument and compiled into the binary, so a running container reports the
  tag it was published under — `1.2.3` from a release tag, `edge` from `main`:

  ```
  msg=starting version=1.2.3 role=all
  ```

- Build provenance is attested and pushed to the registry, so the image can be traced back to the workflow
  run and commit that produced it:

  ```bash
  gh attestation verify oci://ghcr.io/ffurlanetto/confluence-to-doc:1.2.3 --repo ffurlanetto/confluence-to-doc
  ```

## First release

The package is created private. After the first successful run, make it public in the repository's
**Packages** settings if the image is meant to be pullable anonymously — otherwise every deployment needs
an image pull secret.

The Helm chart's default image repository already points at `ghcr.io/ffurlanetto/confluence-to-doc`, so
once `v0.1.0` is published a default install pulls the matching tag.

## Running a release with Docker Compose

[`docker-compose.release.yml`](../docker-compose.release.yml) runs the published image with a PostgreSQL
next to it and builds nothing. It has no mocks: fill in a real OIDC provider, a real Confluence and an
encryption key in `.env` (see `.env.example`) — compose refuses to start until they are set, naming the
one that is missing.

```bash
docker login ghcr.io          # only while the package is private
make up-release               # docker compose -f docker-compose.release.yml up -d
```

The tag defaults to `latest` and the app service is `pull_policy: always`, so `up` picks up a new release
without further ceremony. Pin it for anything that matters:

```bash
C2D_VERSION=1.2.3 docker compose -f docker-compose.release.yml up -d
```

`C2D_VERSION=edge` runs the current `main`. The other variables the file reads are `HTTP_PORT` (default
`8080`) and `POSTGRES_PASSWORD`; everything else in `.env` is passed straight to the container, so
`S3_BUCKET`, `WORD_TEMPLATE_PATH` and the `OTEL_*` variables work as documented in
[configuration.md](configuration.md).

## Deploying a release

```bash
helm upgrade --install confluence-to-doc ./charts/confluence-to-doc \
  -n confluence-to-doc -f charts/confluence-to-doc/values-production.yaml \
  --set image.tag=1.2.3
```

Pinning the digest instead is stronger, and what a policy-controlled cluster will want:

```yaml
image:
  repository: ghcr.io/ffurlanetto/confluence-to-doc@sha256
  tag: "<digest>"
```

## Not covered yet

The Helm chart is not published anywhere: it is installed from a checkout of this repository. Pushing it
as an OCI artifact next to the image (`helm push` to `ghcr.io`) is a small addition when it is wanted.
