# Invoice fonts

Embedded into `templates/invoice.html` as data URIs, so the renderer sidecar
needs no fonts of its own and no network. All three families are SIL OFL 1.1;
the licence texts sit beside the files, and OFL permits embedding them in the
documents they render.

| File | Source |
| --- | --- |
| `inter-400/600/700.woff2` | the app's bundled static Inter TTFs (`shared/src/commonMain/composeResources/font/` in scorrclub), subset |
| `bricolage-800.woff2` | the app's bundled Bricolage Grotesque ExtraBold, subset |
| `bricolage-700.woff2`, `plexmono-400/600.woff2` | the Latin subsets embedded in the invoice design (`all-3-invoices.html`) |

The subsets keep Basic Latin, Latin-1, curly quotes and dashes, and the rupee
sign (U+20B9). The rupee is why Inter and the 800 are cut from the app's full
files rather than taken from the design: the design's own subsets drop it, and
every amount on an invoice starts with one. The design-sourced faces have no ₹
either, so every font stack in the template ends in Inter, and Chromium takes
that one glyph from it.

Regenerate a subset with fontTools:

    python -m fontTools.subset inter_regular.ttf --flavor=woff2 \
      --unicodes="U+0020-007E,U+00A0-00FF,U+2013-2014,U+2018-201E,U+2022,U+2026,U+20B9,U+2192,U+2713,U+2715,U+25CF,U+25C6,U+2605" \
      --layout-features='kern,liga,calt,tnum,case' --output-file=inter-400.woff2
