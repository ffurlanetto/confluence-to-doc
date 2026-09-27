# ADR 0002 — Conversion HTML → PDF/DOCX avec LibreOffice

- Statut : accepté
- Date : 2026-09-27

## Contexte

Confluence fournit le rendu HTML des pages (`body.export_view`). Il faut produire du PDF **et** du Word
fidèles (titres, tableaux, images), en conservant une structure de plan exploitable.

Options étudiées : génération DOCX native en Go (coûteuse à rendre fidèle), Pandoc (bon DOCX, PDF via LaTeX
lourd), Chromium (PDF uniquement), Gotenberg (service supplémentaire), LibreOffice headless.

## Décision

Assembler un HTML unique puis le convertir avec `soffice --headless` (filtre d'import « HTML (StarWriter) »)
vers `writer_pdf_Export` ou `MS Word 2007 XML`. Chaque conversion utilise un profil LibreOffice jetable,
ce qui permet la parallélisation (bornée par le pool de workers).

## Conséquences

- ✅ Un seul moteur pour les deux formats ; les `hN` deviennent de vrais styles « Titre N » dans Word et des
  signets PDF ; sauts de page, tableaux, images en data URI correctement embarqués (vérifié par test).
- ✅ Encapsulé derrière l'interface `converter.Converter` : remplaçable (Gotenberg, Pandoc…) sans impact.
- ⚠️ Image Docker plus lourde (~400 Mo) et ~1–2 s de démarrage par conversion.
- ⚠️ Le CSS est partiellement supporté : le rendu s'appuie sur des attributs HTML simples (ex. `border` des tableaux).
