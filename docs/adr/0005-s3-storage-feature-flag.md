# ADR 0005 — S3 storage enabled by configuration

- Status: accepted
- Date: 2026-09-27

## Context

Local storage requires a shared (RWX) volume between API and worker instances, which complicates
multi-machine deployments (Kubernetes, separate VMs). Object storage (S3 or compatible) removes that
constraint. Switching had to be possible without code changes and without breaking existing deployments.

## Decision

- A second `storage.BlobStore` implementation on the AWS Go SDK v2 (standard, and compatible with
  third-party S3 services through `S3_ENDPOINT` plus path-style addressing, with the default IAM
  credential chain).
- **Implicit feature flag**: `S3_BUCKET` set → S3, otherwise local disk. No separate “mode” variable, to
  rule out inconsistent states (S3 mode without a bucket).
- Upload: the stream is spooled to a temporary file, then sent as a single `PutObject` (replayable body of
  known length, no partial object on failure). The SDK transfer helpers were rejected:
  `feature/s3/manager` is deprecated and its replacement `transfermanager` is not stable (v0.x).
- Download: `HeadObject` followed by lazy ranged GETs, so `http.ServeContent` can serve `Range` requests.
- Downloads are always proxied by the API (ownership check, headers, bucket may stay private and unexposed
  to browsers) rather than using pre-signed URLs.
- One contract test suite covers both backends; S3 is faked in memory (`gofakes3`, a test-only dependency).

## Consequences

- ✅ API and workers can be deployed without a shared volume; the bucket is checked at startup.
- ✅ Existing deployments are unchanged (local storage remains the default).
- ⚠️ Documents flow through the API on download (fine for files of a few MB; pre-signed URLs can be added
  if volume demands it).
- ⚠️ An object larger than 5 GiB would need a multipart upload (well beyond expected sizes).
