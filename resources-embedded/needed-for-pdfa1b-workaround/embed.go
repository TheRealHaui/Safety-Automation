// Package pdfresources embeds the static files required for PDF/A-1b generation.
// They live here (under resources-embedded/) rather than next to the pdf package
// because go:embed forbids ".." in patterns, so files must sit within or below
// the embedding package's own directory.
package pdfresources

import _ "embed"

// LiberationMonoTTF is Liberation Mono Regular 2.1.5 (SIL Open Font License 1.1,
// see LiberationMono-LICENSE.txt), a metric-compatible replacement for Courier.
// Embedded to satisfy PDF/A-1b rule 6.3.4 (full font embedding).
//
//go:embed LiberationMono-Regular.ttf
var LiberationMonoTTF []byte

// SrgbICC is the International Color Consortium's official sRGB2014 profile
// (ICC version 2.0), included unmodified — see sRGB2014-LICENSE.txt.
//
// Embedded to satisfy PDF/A-1b rule 6.2.3.3 (OutputIntent with embedded ICC profile).
// Must stay an ICC v2 profile.
// PDF/A-1b (6.2.2) rejects v4 profiles.
// Do not replace with Ghostscript's sRGB profile as this is AGPL-licensed.
//
//go:embed sRGB2014.icc
var SrgbICC []byte
