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
   Word's built-in heading styles already carry an **outline level**, which is what produces the Word
   navigation pane and the PDF bookmarks. Keep using them rather than inventing look-alike styles: a
   heading style without an outline level renders correctly but yields a PDF with no bookmarks.
4. **Colours** — set them through the document **theme** (Design ▸ Colors) and have the styles reference it
   rather than hard-coding hex values. The theme comes along, so `accent1`, the text colours and the
   hyperlink colour all apply to the export.
5. **Character styles** — define **Hyperlink** (and, if you care, **Strong** and **Emphasis**). Links take
   the template's `Hyperlink` style, which is usually where a colour palette shows most.
6. **Contents styles** — define **TOC 1** to **TOC 9** (and **TOC Heading**) if you want the table of
   contents to follow the company look. The export produces a real Word contents field.
7. **Leave the body empty** (or with a placeholder): the template's own body content is discarded.

A minimal checklist: page setup ✅, header ✅, `Normal` style ✅, `Heading 1`–`Heading 6` styles
(with outline levels) ✅, theme colours ✅, `Hyperlink` style ✅.

### What to expect

- Titles adopt the template's heading styles, so the Word navigation pane and the PDF bookmarks follow the
  company look.
- Body text, tables and captions use the template's default paragraph style, and therefore its font.
- **The colour and font theme applies.** The theme part, the relationship to it and its content type are
  all carried over, so styles that reference `accent1`, `text2`, the hyperlink colour or the major/minor
  theme fonts resolve against the company palette.
- **Style references are matched by name, then by id, ignoring capitalisation.** LibreOffice and Word give
  the same style different ids — a link is `InternetLink` in one and `Hyperlink` in the other, and a
  French Word writes `Titre1` for what it names `heading 1` — while both record the same style *name*,
  which Word does not translate. Word itself matches style names "on spelling and spacing, but not
  necessarily capitalisation", and so does this: LibreOffice writes `Heading 1` where Word writes
  `heading 1`. This is what lets a template authored in another language be honoured at all.
- Any paragraph style the template does **not** name (the converter invents `TableContents`,
  `Quotations`…) is remapped to the template's default paragraph style. This is what makes the company
  font win; it also means a style you did not define cannot be honoured.
- A **character** style the template does not name is carried over with its definition instead — losing it
  would silently drop the bold of `<strong>`, the italics of `<em>` or the monospace of `<code>`.
- A **paragraph** style the template does not name keeps what it says about the text — a table heading is
  bold and centred, a quotation is indented — based on the template's default style, and loses what it
  says about the look. The company font and colours still win.
- The **table of contents** is a real Word field using the template's `TOC 1…9` styles. Word refreshes it
  on open, which is what fills in the page numbers.
- **Document properties** (title, author, description, keywords, classification, and custom properties
  naming the Confluence source) travel with the document into both the DOCX and the PDF.
- Tables keep their visible borders from the export, not from a table style.
- **Tables are laid out again to fit the printable width.** The converter sizes columns from their content
  and readily overflows even its own page — a nine-column Confluence table came out half as wide again as
  the text area. Rather than being cut off, the columns are redistributed: none goes below about 1.2 cm,
  and what that costs is taken from the columns that have room to spare. List indents inside a narrow cell
  are brought back in proportion, or a bullet would be indented past its own text.
- **A paragraph is not split across two pages.** A block — paragraph, list item, quotation, code block or
  heading — that does not fit in what is left of the page moves to the next one whole. A block taller than
  a page still has to break, and it does.
- Bold, italics, inline code and image sizes are preserved. Define `Strong`, `Emphasis` and `Hyperlink`
  character styles in the template if you want the company look for them; otherwise the converter's own
  plain bold, italic and underline are used.

### Fonts in the PDF

Word renders the DOCX with the fonts named by the template, which every workstation that has them will
show correctly. The **PDF is rendered on the server**, so the corporate fonts must be installed in the
image — otherwise LibreOffice silently substitutes a lookalike (Georgia becomes DejaVu Serif, for
instance) and only the PDF looks off.

Install them by extending the image:

```dockerfile
FROM confluence-to-doc:latest
USER root
COPY fonts/*.ttf /usr/share/fonts/truetype/corporate/
RUN fc-cache -f
USER app
```

Licensing is yours to check: many corporate typefaces may not be redistributed inside an image.

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
| Titles do not use the company style             | The style is a custom one, not based on Word's built-in `Heading 1…6`. Styles are matched by their built-in name, so a look-alike style created from scratch cannot be found. |
| Links are not in the company colour             | The template defines no character style named `Hyperlink`. Add one (Word's built-in **Hyperlink** style), and give it a theme colour if you want it to follow the palette. |
| The contents do not look like the company's     | The template defines no `TOC 1`…`TOC 9` styles. Insert a table of contents once in the template and style it; Word creates them. |
| The contents show no page numbers               | Word fills them when it refreshes the field, which it does on open. A PDF never refreshes a field, so it has clickable entries and a bookmark tree instead. |
| Body text keeps a generic font                  | The font was applied to text directly in the template instead of to the `Normal` style. |
| Lists lose their bullets                        | The export brings its own list definitions; check the template's numbering is not corrupted by a Word add-in. |
| The PDF has no bookmarks                        | The template's heading styles carry no outline level. Base them on Word's built-in `Heading 1…6`. |
| The PDF uses the wrong font, the DOCX is fine   | The font is not installed in the image; see [Fonts in the PDF](#fonts-in-the-pdf). |
| Wide tables are cramped                         | There is only so much room: nine columns in a portrait A4 with 2.5 cm margins leave about 1 cm each. Set the template to **landscape**, or widen its margins — the table follows the page it is given. |
| Pages end with a lot of white space             | A block that did not fit was moved whole to the next page rather than being split. Shorter paragraphs and code blocks reduce it. |

If the template cannot be applied to a document, that export fails immediately with an explicit message
rather than being retried — the failure is deterministic — and no half-formatted document is ever
delivered. Exports already downloaded are unaffected.
