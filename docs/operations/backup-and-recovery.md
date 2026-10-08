# Backup and recovery

## What holds state

| Data | Where | Back up? | Lost, it means |
| --- | --- | --- | --- |
| Accounts, preferences, encrypted tokens, Teams URLs, notifications, the export queue and history, the uploaded Word template, the audit trail | **PostgreSQL** | **Yes** — the only thing that must be | Everyone re-enters their token; queue, history, audit trail and template gone |
| **Encryption keys** (`ENCRYPTION_KEY` / `ENCRYPTION_KEYS`) | A Kubernetes Secret, or your secret manager | **Yes, separately** from the database | Every stored token unreadable; each user must enter it again |
| OIDC client secret, SMTP password, S3 credentials | Secrets | Yes, with the keys | The service cannot start until they are reissued |
| Generated documents | Local volume or S3 | **No** | Users export again; they expire after 48 hours anyway |
| Helm values | Your configuration repository | Yes (it is code) | The deployment cannot be reproduced |
| Logs and audit copies shipped to the SIEM | Your log platform | Its own policy | — |

Keep the keys and the database backups **apart**: a backup is only as sensitive as the keys stored next to
it. A stolen dump without the keys exposes no token; a dump with them exposes every one.

## Objectives

| | Target | How |
| --- | --- | --- |
| RPO (data lost at worst) | 5 minutes | Continuous WAL archiving / point-in-time recovery |
| RTO (time to service) | 1 hour | Restore the database, redeploy the chart with the same keys |

These suit an internal tool whose worst loss is a few queued exports and audit events; tighten them with
your database team if the audit trail is a compliance record (the SIEM copy usually is).

## Backing up

**PostgreSQL.** Prefer what your platform offers:

- a managed database (RDS, Cloud SQL, Azure Database…): enable automated backups with point-in-time
  recovery, retention 35 days;
- on Kubernetes, an operator with WAL archiving to object storage (CloudNativePG, Crunchy, Zalando);
- otherwise, at the very least, a nightly logical dump:

  ```sh
  pg_dump --format=custom --no-owner "$DATABASE_URL" > c2d-$(date -u +%F).dump
  ```

  A dump alone gives a 24-hour RPO; add WAL archiving to reach the target above.

The database is small: tokens, preferences and queue rows are tiny; the audit trail grows by roughly a few
hundred bytes per event and is purged after `AUDIT_RETENTION` (one year by default); the uploaded template
is at most 10 MB.

**Keys and secrets.** Store `ENCRYPTION_KEYS` in your secret manager (Vault, a cloud KMS-backed store,
sealed secrets in Git) with its own access control and history. Every key that may still encrypt a token
must be recoverable: when rotating, keep the old key until the rotation is reported complete, and keep a
copy of removed keys for as long as your oldest database backup — restoring an old backup brings back tokens
encrypted with them.

## Restoring

1. **Stop the writers**: `helm upgrade c2d … --set api.replicaCount=0 --set worker.replicaCount=0`, or
   scale both deployments to zero.
2. **Restore PostgreSQL** to the chosen point in time into a new database (or over the old one once it is
   saved aside):

   ```sh
   createdb c2d_restored
   pg_restore --no-owner --dbname=c2d_restored c2d-2026-10-08.dump
   ```

3. **Point the application at it** (`database.url` or its Secret) with the **same encryption keys** — the
   key ring in use when the backup was taken, plus any newer key. Migrations run at startup and are forward
   only; restoring a backup taken before an upgrade is fine with the newer version, not the other way round.
4. **Start** the API and the workers again. Then:
   - exports that were *running* at backup time are picked up again when their lease expires;
   - *succeeded* exports whose file is gone (documents are not backed up) answer *not found* when
     downloaded; the janitor expires them at their normal time. Tell users to export again;
   - sessions in the backup are valid again until they expire: if the incident involves a compromise,
     empty the `sessions` table before starting (`DELETE FROM sessions;`), which signs everyone out.
5. **Check**: sign in, `/admin/queue` lists the queue, an export of the
   [reference documents](reference-documents.md) succeeds, `/admin/audit` shows the restore period.

## Disaster scenarios

| Scenario | Response |
| --- | --- |
| Database lost | Restore (above). Users keep working with their tokens, as long as the keys survived |
| Encryption keys lost | No recovery of tokens is possible, by design. Deploy with a new key; every user enters their token again (the application tells them). Nothing else is lost |
| Keys leaked | Rotate at once ([procedure](runbooks.md#rotate-the-encryption-key)) and ask users to revoke and recreate their Confluence tokens: a leaked key plus any old dump exposes them |
| Storage (S3 or volume) lost | Nothing to restore; recreate the bucket or volume. Pending downloads fail; users export again |
| Cluster lost | Reinstall the chart from your values on another cluster, restore the database, reuse the Secrets |
| Bad release | `helm rollback c2d`. If its migration already ran, roll back the image only if that migration was additive (they are written to be) — otherwise restore |

## Testing

Restore the latest backup into a scratch namespace **every quarter**, run step 5, and note how long it took
against the RTO. A backup never restored is a hope, not a backup.
