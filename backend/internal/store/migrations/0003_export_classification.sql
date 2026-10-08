-- Classification chosen by the user for an export (empty: none). It drives the
-- watermark and is printed in the footer of every page.
ALTER TABLE exports ADD COLUMN classification text NOT NULL DEFAULT '';
