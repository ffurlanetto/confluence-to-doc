# ADR 0001 — File d'attente dans PostgreSQL

- Statut : accepté
- Date : 2026-09-27

## Contexte

La génération des documents est longue (crawl Confluence + conversion) et doit être asynchrone, avec un
mécanisme de file pour contenir la charge. Options : Redis (asynq), RabbitMQ, PostgreSQL.

## Décision

Utiliser la table `exports` comme file : réservation via `SELECT … FOR UPDATE SKIP LOCKED`, bail renouvelable
(`locked_until`), écritures conditionnées au propriétaire (`locked_by`), tentatives avec backoff.

## Conséquences

- ✅ Une seule dépendance d'infrastructure, transactions cohérentes entre l'état métier et la file,
  quota par utilisateur vérifié atomiquement (verrou consultatif).
- ✅ Tolérance aux pannes : un job abandonné est repris à l'expiration du bail.
- ⚠️ Scrutation (2 s par défaut) ; suffisant pour ce volume. `LISTEN/NOTIFY` possible plus tard.
- ⚠️ Pour des milliers de jobs/seconde, un broker dédié serait préférable (hors besoin actuel).
