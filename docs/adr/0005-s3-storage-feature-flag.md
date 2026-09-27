# ADR 0005 — Stockage S3 activé par configuration

- Statut : accepté
- Date : 2026-09-27

## Contexte

Le stockage local impose un volume partagé (RWX) entre les instances API et worker, ce qui complique le
déploiement multi-machines (Kubernetes, VM séparées). Le stockage objet S3 (ou compatible) lève cette contrainte.
Il faut pouvoir basculer sans changer le code ni casser les déploiements existants.

## Décision

- Deuxième implémentation de `storage.BlobStore` sur le SDK AWS Go v2 (standard, compatible avec les services
  S3 tiers via `S3_ENDPOINT` + adressage path-style, chaîne d'identifiants IAM par défaut).
- **Feature flag implicite** : `S3_BUCKET` défini → S3, sinon disque local. Pas de variable « mode » séparée,
  pour éviter les états incohérents (mode S3 sans bucket).
- Envoi : le flux est d'abord écrit dans un fichier temporaire puis envoyé en un seul `PutObject`
  (corps rejouable de taille connue, aucun objet partiel en cas d'échec). Les gestionnaires de transfert du SDK
  ont été écartés : `feature/s3/manager` est déprécié et son remplaçant `transfermanager` n'est pas stable (v0.x).
- Lecture : `HeadObject` puis GET partiels paresseux, pour que `http.ServeContent` gère les requêtes `Range`.
- Téléchargements toujours relayés par l'API (contrôle d'appartenance, en-têtes, bucket pouvant rester privé et
  non exposé au navigateur), plutôt que des URL pré-signées.
- Une suite de tests de contrat commune aux deux backends ; S3 simulé en mémoire (`gofakes3`, dépendance de test).

## Conséquences

- ✅ API et workers peuvent être déployés sans volume partagé ; bucket vérifié dès le démarrage.
- ✅ Déploiements existants inchangés (stockage local par défaut).
- ⚠️ Les documents transitent par l'API au téléchargement (acceptable pour des fichiers de quelques Mo ;
  des URL pré-signées pourront être ajoutées si le volume l'exige).
- ⚠️ Un objet de plus de 5 Gio nécessiterait un envoi multipart (hors des volumes attendus).
