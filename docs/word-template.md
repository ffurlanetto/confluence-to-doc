# Company Word template

Set `WORD_TEMPLATE_PATH` to a `.docx` or `.dotx` file and every generated document — Word **and** PDF —
comes out in the company format: header, footer, fonts, colours and page layout.

Without that variable the application keeps its built-in styling, so existing deployments are unchanged.

## How it works

The template is the base of the result. The converter turns the exported pages into a plain DOCX, and the
body of that document is then injected into the template package:

| Comes from the template        | Comes from the export                          |
| ------------------------------ | ----------------------------------------------- |
| Styles and default fonts       | Text, tables, images, links                     |
| Theme (fonts, colours)         | Heading levels mirroring the Confluence tree    |
| Headers and footers            | Document title and metadata                     |
| Page size, margins, orientation| List numbering definitions                      |

Because a PDF is produced from the templated DOCX, both formats are identical in layout.

The template is read and validated once, at startup: a missing or malformed file stops the process with an
explicit error instead of failing every export later. The active template is shown on the **Preferences**
screen and logged at startup:

```
document template: company Word template  file=acme-template.docx default_paragraph_style=Normal styles=42
```

## Preparing the template

Author it in Word like any normal document, then save as `.dotx` (or `.docx`):

1. **Page layout** — set the page size, orientation and margins. These are taken from the template, so the
   export inherits them.
2. **Header and footer** — put the logo, document title, confidentiality notice and page numbers there.
   They are copied as they are, including images. Automatic page numbers keep working.
3. **Fonts and colours** — set them on the *styles*, not on loose text: change the **Normal** style for
   body text and **Heading 1** to **Heading 6** for titles. The export uses `Heading 1…6` for the page
   hierarchy and `Normal` for everything else.
4. **Leave the body empty** (or with a placeholder): the template's own body content is discarded.

A minimal checklist: page setup ✅, header ✅, `Normal` style ✅, `Heading 1`–`Heading 6` styles ✅.

### What to expect

- Titles adopt the template's heading styles, so the Word navigation pane and the PDF bookmarks follow the
  company look.
- Body text, tables and captions use the template's default paragraph style, and therefore its font.
- Any style the template does **not** define (the converter invents names such as `BodyText` or
  `TableContents`) is remapped to the template's default paragraph style. This is what makes the company
  font win; it also means a style you did not define cannot be honoured.
- Tables keep their visible borders from the export, not from a table style.
- Bold, italics, code blocks and image sizes are direct formatting and are preserved.

## Rolling it out

The template is a deployment artefact, like a configuration file: mount it into the container (volume,
ConfigMap, secret) and point `WORD_TEMPLATE_PATH` at it.

```yaml
# docker compose
services:
  app:
    environment:
      WORD_TEMPLATE_PATH: /etc/confluence-to-doc/company-template.dotx
    volumes:
      - ./company-template.dotx:/etc/confluence-to-doc/company-template.dotx:ro
```

Every instance that runs workers needs the file. Updating it requires a restart, which is also when it is
re-validated.

## Troubleshooting

| Symptom                                        | Cause and fix                                                                 |
| ---------------------------------------------- | ----------------------------------------------------------------------------- |
| Startup fails with `invalid Word template`      | The file is not a Word package, or misses `styles.xml` / page setup. Re-save it from Word. |
| Startup fails with `template has no <w:sectPr>` | The template has no page setup. Open it in Word, adjust any margin, save again. |
| The header is missing                           | It was defined in Word as a *first page* header only, and the export's first page uses the default header. Define the default header too. |
| Titles do not use the company style             | The template defines translated style names but not the `Heading 1…6` style ids. Base the styles on Word's built-in headings rather than creating new ones. |
| Body text keeps a generic font                  | The font was applied to text directly in the template instead of to the `Normal` style. |
| Lists lose their bullets                        | The export brings its own list definitions; check the template's numbering is not corrupted by a Word add-in. |

If the template cannot be applied to a document, that export fails immediately with an explicit message
rather than being retried — the failure is deterministic — and no half-formatted document is ever
delivered. Exports already downloaded are unaffected.
