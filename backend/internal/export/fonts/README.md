# Export fonts

Embedded with `go:embed` (`../fonts.go`), so PDF rendering is identical in every environment. Nothing is read from the host.

| File | Family / style | Version | Source | License |
|---|---|---|---|---|
| `NotoSans-Regular.ttf`, `NotoSans-Bold.ttf` | Noto Sans Regular / Bold (primary) | 2.015 (name table) | Noto Project, https://github.com/notofonts/latin-greek-cyrillic | SIL OFL 1.1, `OFL.txt` |
| `DejaVuSans.ttf`, `DejaVuSans-Bold.ttf` | DejaVu Sans Book / Bold (fallback) | 2.37 | official release `dejavu-fonts-ttf-2.37.tar.bz2` from https://github.com/dejavu-fonts/dejavu-fonts/releases/tag/version_2_37 (archive sha256 `fa9ca4d13871dd122f61258a80d01751d603b4d3ee14095d65453b4e846e17d7`), files `ttf/DejaVuSans.ttf`, `ttf/DejaVuSans-Bold.ttf` unmodified | Bitstream Vera / Arev license, `DejaVu-LICENSE.txt` (the release's `LICENSE`) |

sha256 of the embedded files:

```
478c558ea716033cd60c03438f628dfa75694dcf6b5f6d505a2f05fd2b4f3823  NotoSans-Regular.ttf
1df075a380fc7cb898acf64c1f7b3b4dd780de3caa860178bf929de35817a913  NotoSans-Bold.ttf
7da195a74c55bef988d0d48f9508bd5d849425c1770dba5d7bfc6ce9ed848954  DejaVuSans.ttf
e6476c1b80502924294eed40894c5b18e06c181444ca953e5334262df9c27724  DejaVuSans-Bold.ttf
```

## Fallback

Glyph coverage comes from each file's Unicode BMP cmap (format 4), read once at init. Text is split into runs per rune: a rune Noto Sans (same style) lacks but DejaVu Sans has is drawn with DejaVu Sans (e.g. arrows `→`, math `∑ ≤`, box drawing `─`, check marks `✓`); spaces and runes neither font has stay in the current run. Runes above U+FFFF are replaced with `?` (fpdf limit).

- **PDF**: both fonts are subset-embedded; DejaVu Sans is only registered (and embedded) when a document uses it.
- **DOCX**: fonts are not embedded (Word embedding is unreliable across Word, Pages, Google Docs and LibreOffice). `styles.xml` defaults all runs to `Noto Sans` (`ascii`, `hAnsi`, `eastAsia`, `cs`); runs that need the fallback name `DejaVu Sans` explicitly. Viewers without these fonts substitute their own, so DOCX appearance can differ by machine; the text is the same.
