package pdf

import (
	"bytes"
	"crypto/md5"
	"fmt"
	"strings"
	"time"

	"github.com/TheRealHaui/Safety-Automation/internal/backend/database"
	pdfresources "github.com/TheRealHaui/Safety-Automation/resources-embedded/needed-for-pdfa1b-workaround"
)

//This class creates a fully compliant pdf/a-1b pdf.
// Verify with https://demo.verapdf.org/ the output of the pdf file.
//
// PDF GENERATION - ARCHITECTURE NOTES
//
// Current implementation: PDF/A-1b compliant output, written entirely from scratch
// without any PDF library. While it works, this is not a maintainable approach.
//
// DO NOT improve, refactor or clean up this file by hand. If anything shall get added
// for example text output or something linke that let Claude do it! If the entire
// structure shall get changed AND Claude cannot do it on its own rewrite the entire
// code using github.com/signintech/gopdf  — any manual fixes here are wasted effort.
// Again this is much too low level to fix it by hand.
//
// If this source code file gets removed all artifacts from
// /resources-embedded/needed-for-pdfa1b-workaround either have to get removed
// entirely too or re-used for the mentioned Ghostscript approach. See below.
//
// ----------------------------------------------------------------------------
// PLANNED REWRITE
// ----------------------------------------------------------------------------
//
// Replace the hand-written implementation with:
//   1. github.com/signintech/gopdf  — for PDF generation
//   2. Ghostscript                  — for PDF/A-1b post-processing
//
// This is the de-facto standard approach for this pattern.
//
// Ghostscript command:
//   gs -dPDFA=1 -dBATCH -dNOPAUSE -dNOOUTERSAVE \
//      -sProcessColorModel=DeviceRGB \
//      -sDEVICE=pdfwrite \
//      -dPDFACompatibilityPolicy=1 \
//      -sOutputFile=output_pdfa.pdf \
//      input.pdf
//
// Dockerfile changes required:
//   - Install Ghostscript:
//       RUN apt-get install -y ghostscript
//   - Ensure an ICC color profile is available (Ghostscript does not always
//     bundle one):
//       RUN apt-get install -y icc-profiles-free
//     Profile will be at: /usr/share/color/icc/colord/sRGB.icc
//     Pass to Ghostscript via: -sICCProfiles=/usr/share/color/icc/colord
//
// Local machine:
//   Install Ghostscript via package manager (apt, brew, etc.) for local testing.
//
// ----------------------------------------------------------------------------
// ALTERNATIVE OPTIONS (for reference)
// ----------------------------------------------------------------------------
//
// Option 1 — UniDoc/UniPDF (commercial)
//   Native Go library with built-in PDF/A-1b support. Requires a paid license.
//   https://unidoc.io
//
// Option 2 — Gotenberg (Docker sidecar)
//   Separate container that accepts HTML and renders to PDF/A via headless browser.
//   Adds infrastructure complexity on OpenShift.
//   https://gotenberg.dev
//
// ----------------------------------------------------------------------------
// VALIDATION
// ----------------------------------------------------------------------------
//
// After the rewrite, validate output with VeraPDF — the de-facto standard
// PDF/A validator, free and open source.
//   Online demo:  https://demo.verapdf.org/
//   CLI/CI use:   https://verapdf.org/
//
// Run validation at least once during development before considering the
// PDF/A-1b implementation complete.

// ReportData contains all data needed to render the audit report PDF.
type ReportData struct {
	TestRun     database.TestRun
	Logs        []database.TestRunLog
	URLs        []database.TestRunURL
	Findings    []database.TestRunFinding
	Verdicts    []database.TestRunVerdict
	GeneratedAt time.Time
}

// GenerateAuditReport produces a business audit report PDF for the given test run.
// Returns raw PDF bytes. Not stored in DB - generated on demand.
func GenerateAuditReport(data ReportData) ([]byte, error) {
	// Build the text content of the report
	var content strings.Builder
	writeCoverSection(&content, data)
	writeSummarySection(&content, data)
	writeFindingsSection(&content, data)
	writeVerdictSection(&content, data)
	writeLogSection(&content, data)

	return createTextPDF(content.String())
}

func writeCoverSection(sb *strings.Builder, data ReportData) {
	sb.WriteString("RESILIENCE AUDIT REPORT\n")
	sb.WriteString("=======================\n\n")
	sb.WriteString(fmt.Sprintf("Classification  : Internal / Confidential\n"))
	sb.WriteString(fmt.Sprintf("Report Reference: RUN-%06d\n", data.TestRun.ID))
	sb.WriteString(fmt.Sprintf("Generated At    : %s\n\n", data.GeneratedAt.Format("2006-01-02 15:04:05 UTC")))
	sb.WriteString("WORKLOAD UNDER TEST\n")
	sb.WriteString("-------------------\n")
	sb.WriteString(fmt.Sprintf("  Name       : %s\n", data.TestRun.WorkloadName))
	sb.WriteString(fmt.Sprintf("  Namespace  : %s\n", data.TestRun.WorkloadNamespace))
	sb.WriteString(fmt.Sprintf("  DeploymentKind       : %s\n\n", data.TestRun.WorkloadKind))
	sb.WriteString("TEST EXECUTION\n")
	sb.WriteString("--------------\n")
	sb.WriteString(fmt.Sprintf("  Scenario   : %s\n", data.TestRun.ScenarioName))
	sb.WriteString(fmt.Sprintf("  Initiated  : %s\n", data.TestRun.StartedAt.Format("2006-01-02 15:04:05 UTC")))
	sb.WriteString(fmt.Sprintf("  Completed  : %s\n", finishedAtStr(data.TestRun)))
	sb.WriteString(fmt.Sprintf("  Duration   : %.0f seconds\n", data.TestRun.DurationSeconds))
	sb.WriteString(fmt.Sprintf("  Initiated by: %s\n\n", data.TestRun.StartedBy))
	sb.WriteString("OVERALL RESULT\n")
	sb.WriteString("--------------\n")
	sb.WriteString(fmt.Sprintf("  %s\n\n", statusLabel(string(data.TestRun.Status))))
	sb.WriteString(strings.Repeat("-", 72) + "\n\n")
}

func writeSummarySection(sb *strings.Builder, data ReportData) {
	sb.WriteString("EXECUTIVE SUMMARY\n")
	sb.WriteString("=================\n\n")

	if data.TestRun.ResultSummary != "" {
		sb.WriteString(data.TestRun.ResultSummary)
		sb.WriteString("\n\n")
	}

	if len(data.URLs) > 0 {
		sb.WriteString("URLS UNDER LOAD\n")
		sb.WriteString("---------------\n")
		for _, u := range data.URLs {
			sb.WriteString(fmt.Sprintf("  [%-7s] %s\n", u.HTTPMethod, u.URLString))
			contentType := u.ContentType
			if contentType == "" {
				contentType = "(none provided by user/tester)"
			}
			sb.WriteString(fmt.Sprintf("           Content-Type: %s\n", contentType))
			requestBody := u.RequestBody
			if requestBody == "" {
				requestBody = "(none provided by user/tester)"
			}
			sb.WriteString("           Body:\n")
			for _, bodyLine := range strings.Split(requestBody, "\n") {
				sb.WriteString(fmt.Sprintf("             %s\n", bodyLine))
			}
		}
		sb.WriteString("\n")
	}

	counts := map[string]int{}
	for _, l := range data.Logs {
		counts[l.LogLevel]++
	}
	sb.WriteString("LOG STATISTICS\n")
	sb.WriteString("--------------\n")
	sb.WriteString(fmt.Sprintf("  Total entries : %d\n", len(data.Logs)))
	sb.WriteString(fmt.Sprintf("  INFO          : %d\n", counts["INFO"]))
	sb.WriteString(fmt.Sprintf("  WARN          : %d\n", counts["WARN"]))
	sb.WriteString(fmt.Sprintf("  ERROR         : %d\n", counts["ERROR"]))
	sb.WriteString(fmt.Sprintf("  DEBUG         : %d\n\n", counts["DEBUG"]))
	sb.WriteString(strings.Repeat("-", 72) + "\n\n")
}

func writeFindingsSection(sb *strings.Builder, data ReportData) {
	sb.WriteString("FINDINGS\n")
	sb.WriteString("========\n\n")
	if len(data.Findings) == 0 {
		sb.WriteString("No findings recorded for this test run.\n\n")
		sb.WriteString(strings.Repeat("-", 72) + "\n\n")
		return
	}
	for _, f := range data.Findings {
		sb.WriteString(fmt.Sprintf("  %-55s: %s (%s)\n", f.FindingTypeName, f.ResultValue, f.ResultValueType))
	}
	sb.WriteString("\n")
	sb.WriteString(strings.Repeat("-", 72) + "\n\n")
}

func writeVerdictSection(sb *strings.Builder, data ReportData) {
	sb.WriteString("RESILIENCE VERDICTS\n")
	sb.WriteString("===================\n\n")
	if len(data.Verdicts) == 0 {
		sb.WriteString("Verdict computation not yet implemented.\n\n")
		sb.WriteString(strings.Repeat("-", 72) + "\n\n")
		return
	}
	for _, v := range data.Verdicts {
		sb.WriteString(fmt.Sprintf("  %-40s [%-8s] %s\n", v.VerdictName, v.VerdictConclusion, v.VerdictText))
	}
	sb.WriteString("\n")
	sb.WriteString(strings.Repeat("-", 72) + "\n\n")
}

func writeLogSection(sb *strings.Builder, data ReportData) {
	sb.WriteString("EXECUTION LOG\n")
	sb.WriteString("=============\n\n")
	if len(data.Logs) == 0 {
		sb.WriteString("No log entries recorded.\n")
		return
	}
	for _, entry := range data.Logs {
		sb.WriteString(fmt.Sprintf("[%s] [%-5s] %s\n",
			entry.LogTimestamp.Format("2006-01-02 15:04:05.000"),
			entry.LogLevel,
			entry.Message,
		))
	}
}

// createTextPDF renders plain text into a multi-page PDF/A-1b (ISO 19005-1) document.
//
// PDF/A-1b compliance is required for regulated-industry customers (banks, telecoms)
// that mandate long-term archival formats. The five VeraPDF failures in the previous
// plain PDF 1.4 output have been fixed here without any external runtime dependency:
//
//   - 6.1.2  Binary comment (%\xE2\xE3\xCF\xD3) after the %PDF header signals that
//     the file contains binary data; required by the spec and some archival tools.
//   - 6.1.3  /ID array in the trailer (two identical MD5 fingerprints of document
//     metadata); required so archival systems can identify and deduplicate documents.
//   - 6.7.2  XMP metadata stream with pdfaid:part=1 and pdfaid:conformance=B,
//     referenced from the Catalog — the machine-readable conformance declaration.
//   - 6.3.4  Full font embedding. /Courier was referenced by name only (a standard
//     Type 1 font) — PDF/A-1b allows no name-only references, even for the 14
//     standard PDF fonts. Liberation Mono Regular (Apache 2.0) is embedded as a
//     TrueType font via FontFile2; it is metric-compatible with Courier so all
//     existing layout constants remain valid.
//   - 6.2.3.3 OutputIntent with an embedded sRGB ICC profile (/GTS_PDFA1 intent)
//     provides a colorimetry anchor; required even for pure black-on-white documents.
//
// See https://trello.com/c/OSWL1xOm for full scope, history, and VeraPDF output.
//
// Layout constants:
//   - A4 page: 595 × 842 points
//   - Liberation Mono 8pt monospace: character advance ≈ 4.8pt (600/1000 × 8pt)
//   - Usable width: 595 − 40 (left) − 40 (right) = 515pt → 107 chars max
//   - Leading: 12pt; usable height: 800 − 40 (bottom margin) = 760pt → 63 lines/page
//
// Object numbering for N pages:
//
//	1         — Catalog
//	2         — Pages
//	3 + 2*i   — content stream for page i  (i = 0 … N-1)
//	4 + 2*i   — page object for page i
//	3 + 2*N   — Font (TrueType /LiberationMono, WinAnsiEncoding)
//	4 + 2*N   — FontDescriptor
//	5 + 2*N   — FontFile2 stream (embedded TTF binary)
//	6 + 2*N   — ToUnicode CMap stream
//	7 + 2*N   — ICC profile stream (sRGB)
//	8 + 2*N   — OutputIntent (/GTS_PDFA1)
//	9 + 2*N   — XMP metadata stream
func createTextPDF(content string) ([]byte, error) {
	const (
		maxLineChars = 105 // chars that fit in usable width at Liberation Mono 8pt
		linesPerPage = 63  // lines that fit between top (y=800) and bottom margin (y=40)
		marginLeft   = 40
		marginTop    = 800
		leading      = 12
	)

	// ── 1. Wrap long lines ────────────────────────────────────────────────────
	// Strip \r so CRLF data from Oracle CLOBs (ResultSummary, log messages,
	// request bodies) does not inject char code 13 into the PDF stream.
	// Char 13 is below FirstChar=32 and has no glyph in Liberation Mono, which
	// causes VeraPDF rules 6.3.5 and 6.3.6 to fail. Any other character outside
	// the printable ASCII range (32–126) is replaced with '?' for the same reason.
	content = strings.ReplaceAll(content, "\r", "")
	// Replace tabs with spaces — tab (byte 9) is outside the printable ASCII range
	// (32–126) and would otherwise be sanitised to '?' below, mangling JSON bodies.
	content = strings.ReplaceAll(content, "\t", "  ")
	rawLines := strings.Split(content, "\n")
	var allLines []string
	for _, line := range rawLines {
		// Decode UTF-8 runes and map each to its Windows-1252 byte.
		// Characters outside WinAnsiEncoding (e.g. CJK, emoji) become '?'.
		sanitised := make([]byte, 0, len(line))
		for _, r := range line {
			if b, ok := winAnsiEncode(r); ok {
				sanitised = append(sanitised, b)
			} else {
				sanitised = append(sanitised, '?')
			}
		}
		line = string(sanitised)
		for len(line) > maxLineChars {
			allLines = append(allLines, line[:maxLineChars])
			line = "  " + line[maxLineChars:] // 2-space indent marks continuation
		}
		allLines = append(allLines, line)
	}

	// ── 2. Paginate ───────────────────────────────────────────────────────────
	var pages [][]string
	for i := 0; i < len(allLines); i += linesPerPage {
		end := i + linesPerPage
		if end > len(allLines) {
			end = len(allLines)
		}
		pages = append(pages, allLines[i:end])
	}
	if len(pages) == 0 {
		pages = [][]string{{}}
	}
	numPages := len(pages)

	// ── 3. Build one content stream per page ─────────────────────────────────
	var streams [][]byte
	for _, pageLines := range pages {
		var s bytes.Buffer
		s.WriteString("BT\n")
		s.WriteString("/F1 8 Tf\n")
		fmt.Fprintf(&s, "%d %d Td\n", marginLeft, marginTop)
		fmt.Fprintf(&s, "%d TL\n", leading)
		for _, line := range pageLines {
			// Escape the three PDF string special characters.
			line = strings.ReplaceAll(line, "\\", "\\\\")
			line = strings.ReplaceAll(line, "(", "\\(")
			line = strings.ReplaceAll(line, ")", "\\)")
			fmt.Fprintf(&s, "(%s) Tj T*\n", line)
		}
		s.WriteString("ET\n")
		streams = append(streams, s.Bytes())
	}

	// ── 4. Object numbers (fixed layout, see function comment) ───────────────
	fontObjNum := 3 + 2*numPages
	fontDescObjNum := fontObjNum + 1
	fontFile2ObjNum := fontObjNum + 2
	toUnicodeObjNum := fontObjNum + 3
	iccObjNum := fontObjNum + 4
	outputIntentObjNum := fontObjNum + 5
	xmpObjNum := fontObjNum + 6
	totalObjs := xmpObjNum

	offsets := make([]int, totalObjs+1) // 1-based; index 0 unused

	// ── 5. Build PDF binary ───────────────────────────────────────────────────
	var pdf bytes.Buffer

	// Fix 6.1.2: binary comment immediately after the header tells archival tools
	// and file-type detectors that the file contains 8-bit binary data.
	pdf.WriteString("%PDF-1.4\n")
	pdf.Write([]byte("%\xE2\xE3\xCF\xD3\n"))

	// Obj 1: Catalog — references Pages, XMP metadata, and OutputIntents array.
	offsets[1] = pdf.Len()
	fmt.Fprintf(&pdf,
		"1 0 obj\n<</Type /Catalog /Pages 2 0 R /Metadata %d 0 R /OutputIntents [%d 0 R]>>\nendobj\n",
		xmpObjNum, outputIntentObjNum,
	)

	// Obj 2: Pages — Kids lists every page object (4, 6, 8, …)
	offsets[2] = pdf.Len()
	var kids strings.Builder
	for i := 0; i < numPages; i++ {
		if i > 0 {
			kids.WriteString(" ")
		}
		fmt.Fprintf(&kids, "%d 0 R", 4+2*i)
	}
	fmt.Fprintf(&pdf, "2 0 obj\n<</Type /Pages /Kids [%s] /Count %d>>\nendobj\n",
		kids.String(), numPages)

	// Obj pairs: content stream (3+2i) and page object (4+2i) for each page.
	for i, streamBytes := range streams {
		contentObjNum := 3 + 2*i
		pageObjNum := 4 + 2*i

		offsets[contentObjNum] = pdf.Len()
		fmt.Fprintf(&pdf, "%d 0 obj\n<</Length %d>>\nstream\n", contentObjNum, len(streamBytes))
		pdf.Write(streamBytes)
		pdf.WriteString("\nendstream\nendobj\n")

		offsets[pageObjNum] = pdf.Len()
		fmt.Fprintf(&pdf, "%d 0 obj\n<</Type /Page /Parent 2 0 R /MediaBox [0 0 595 842]\n", pageObjNum)
		fmt.Fprintf(&pdf, "  /Resources <</Font <</F1 %d 0 R>>>> /Contents %d 0 R>>\nendobj\n",
			fontObjNum, contentObjNum)
	}

	// ── 6. Font objects (Fix 6.3.4: full embedding, no name-only reference) ──
	// Liberation Mono is a TrueType font — /Subtype /TrueType, embedded via
	// FontFile2. WinAnsiEncoding (Windows-1252) covers all printable ASCII and
	// common Latin-1 characters used in report text.

	// Widths: Liberation Mono is strictly monospace, every glyph is 600 units.
	// FirstChar=32 (space), LastChar=255 — covers all WinAnsiEncoding characters.
	var widths strings.Builder
	for ch := 32; ch <= 255; ch++ {
		if ch > 32 {
			widths.WriteString(" ")
		}
		widths.WriteString("600")
	}

	offsets[fontObjNum] = pdf.Len()
	fmt.Fprintf(&pdf,
		"%d 0 obj\n<</Type /Font /Subtype /TrueType /BaseFont /LiberationMono\n"+
			"  /Encoding /WinAnsiEncoding\n"+
			"  /FirstChar 32 /LastChar 255\n"+
			"  /Widths [%s]\n"+
			"  /FontDescriptor %d 0 R\n"+
			"  /ToUnicode %d 0 R>>\nendobj\n",
		fontObjNum, widths.String(), fontDescObjNum, toUnicodeObjNum,
	)

	// FontDescriptor: metrics allow renderers to synthesise a substitute if the
	// embedded font cannot be decoded; Flags=33 = Fixed(1) + NonSymbolic(32).
	offsets[fontDescObjNum] = pdf.Len()
	fmt.Fprintf(&pdf,
		"%d 0 obj\n<</Type /FontDescriptor /FontName /LiberationMono\n"+
			"  /Flags 33\n"+
			"  /FontBBox [-558 -307 1600 1007]\n"+
			"  /ItalicAngle 0\n"+
			"  /Ascent 860\n"+
			"  /Descent -240\n"+
			"  /CapHeight 671\n"+
			"  /StemV 80\n"+
			"  /FontFile2 %d 0 R>>\nendobj\n",
		fontDescObjNum, fontFile2ObjNum,
	)

	// FontFile2: the raw TTF binary, referenced by the FontDescriptor above.
	offsets[fontFile2ObjNum] = pdf.Len()
	fmt.Fprintf(&pdf,
		"%d 0 obj\n<</Length %d /Length1 %d>>\nstream\n",
		fontFile2ObjNum, len(pdfresources.LiberationMonoTTF), len(pdfresources.LiberationMonoTTF),
	)
	pdf.Write(pdfresources.LiberationMonoTTF)
	pdf.WriteString("\nendstream\nendobj\n")

	// ToUnicode CMap: maps glyph codes 0x20–0x7E to their Unicode code points.
	// Required by PDF/A-1b so that text is extractable and searchable; without
	// this an archival system cannot recover the original characters from glyphs.
	var cmap bytes.Buffer
	cmap.WriteString("/CIDInit /ProcSet findresource begin\n")
	cmap.WriteString("12 dict begin\n")
	cmap.WriteString("begincmap\n")
	cmap.WriteString("/CMapName /F1-UTF16 def\n")
	cmap.WriteString("/CMapType 2 def\n")
	cmap.WriteString("/WMode 0 def\n")
	cmap.WriteString("1 begincodespacerange\n<20> <FF>\nendcodespacerange\n")
	// Build the Windows-1252 forward map (byte → Unicode code point).
	// Bytes 0x20–0x7E and 0xA0–0xFF are identical to their Unicode values (Latin-1).
	// Bytes 0x80–0x9F are the Windows-1252 special chars; undefined slots (0x81,
	// 0x8D, 0x8F, 0x90, 0x9D) are omitted — we never emit those byte values.
	var win1252 [256]rune
	for i := 0x20; i <= 0x7E; i++ {
		win1252[i] = rune(i)
	}
	for i := 0xA0; i <= 0xFF; i++ {
		win1252[i] = rune(i)
	}
	win1252[0x80] = 0x20AC // €
	win1252[0x82] = 0x201A // ‚
	win1252[0x83] = 0x0192 // ƒ
	win1252[0x84] = 0x201E // „
	win1252[0x85] = 0x2026 // …
	win1252[0x86] = 0x2020 // †
	win1252[0x87] = 0x2021 // ‡
	win1252[0x88] = 0x02C6 // ˆ
	win1252[0x89] = 0x2030 // ‰
	win1252[0x8A] = 0x0160 // Š
	win1252[0x8B] = 0x2039 // ‹
	win1252[0x8C] = 0x0152 // Œ
	win1252[0x8E] = 0x017D // Ž
	win1252[0x91] = 0x2018 // '
	win1252[0x92] = 0x2019 // '
	win1252[0x93] = 0x201C // "
	win1252[0x94] = 0x201D // "
	win1252[0x95] = 0x2022 // •
	win1252[0x96] = 0x2013 // –
	win1252[0x97] = 0x2014 // —
	win1252[0x98] = 0x02DC // ˜
	win1252[0x99] = 0x2122 // ™
	win1252[0x9A] = 0x0161 // š
	win1252[0x9B] = 0x203A // ›
	win1252[0x9C] = 0x0153 // œ
	win1252[0x9E] = 0x017E // ž
	win1252[0x9F] = 0x0178 // Ÿ
	var cmapEntries [][2]int
	for b := 0x20; b <= 0xFF; b++ {
		if win1252[b] != 0 {
			cmapEntries = append(cmapEntries, [2]int{b, int(win1252[b])})
		}
	}
	fmt.Fprintf(&cmap, "%d beginbfchar\n", len(cmapEntries))
	for _, e := range cmapEntries {
		fmt.Fprintf(&cmap, "<%02X> <%04X>\n", e[0], e[1])
	}
	cmap.WriteString("endbfchar\n")
	cmap.WriteString("endcmap\n")
	cmap.WriteString("CMapName currentdict /CMap defineresource pop\n")
	cmap.WriteString("end\nend\n")
	cmapBytes := cmap.Bytes()

	offsets[toUnicodeObjNum] = pdf.Len()
	fmt.Fprintf(&pdf, "%d 0 obj\n<</Length %d>>\nstream\n", toUnicodeObjNum, len(cmapBytes))
	pdf.Write(cmapBytes)
	pdf.WriteString("\nendstream\nendobj\n")

	// ── 7. ICC profile + OutputIntent (Fix 6.2.3.3) ──────────────────────────
	// The sRGB v2 profile from colord provides the colorimetry declaration that
	// PDF/A-1b requires even for documents that only use black text on white.
	// /N 3 = three colour components (R, G, B). /Alternate is advisory only.
	offsets[iccObjNum] = pdf.Len()
	fmt.Fprintf(&pdf,
		"%d 0 obj\n<</Length %d /N 3 /Alternate /DeviceRGB>>\nstream\n",
		iccObjNum, len(pdfresources.SrgbICC),
	)
	pdf.Write(pdfresources.SrgbICC)
	pdf.WriteString("\nendstream\nendobj\n")

	offsets[outputIntentObjNum] = pdf.Len()
	fmt.Fprintf(&pdf,
		"%d 0 obj\n<</Type /OutputIntent /S /GTS_PDFA1\n"+
			"  /OutputConditionIdentifier (sRGB IEC61966-2.1)\n"+
			"  /DestOutputProfile %d 0 R>>\nendobj\n",
		outputIntentObjNum, iccObjNum,
	)

	// ── 8. XMP metadata stream (Fix 6.7.2) ───────────────────────────────────
	// pdfaid:part=1 / pdfaid:conformance=B is the machine-readable claim that
	// this file conforms to PDF/A-1b. VeraPDF checks this stream and compares it
	// against the actual document structure.
	xmpContent := `<?xpacket begin='` + "\xEF\xBB\xBF" + `' id='W5M0MpCehiHzreSzNTczkc9d'?>
<x:xmpmeta xmlns:x='adobe:ns:meta/'>
  <rdf:RDF xmlns:rdf='http://www.w3.org/1999/02/22-rdf-syntax-ns#'>
    <rdf:Description rdf:about='' xmlns:pdfaid='http://www.aiim.org/pdfa/ns/id/'>
      <pdfaid:part>1</pdfaid:part>
      <pdfaid:conformance>B</pdfaid:conformance>
    </rdf:Description>
  </rdf:RDF>
</x:xmpmeta>
<?xpacket end='w'?>`
	xmpBytes := []byte(xmpContent)

	offsets[xmpObjNum] = pdf.Len()
	// /Type /Metadata and /Subtype /XML are required by the XMP spec.
	fmt.Fprintf(&pdf,
		"%d 0 obj\n<</Type /Metadata /Subtype /XML /Length %d>>\nstream\n",
		xmpObjNum, len(xmpBytes),
	)
	pdf.Write(xmpBytes)
	pdf.WriteString("\nendstream\nendobj\n")

	// ── 9. Cross-reference table ──────────────────────────────────────────────
	xrefOffset := pdf.Len()
	fmt.Fprintf(&pdf, "xref\n0 %d\n", totalObjs+1)
	pdf.WriteString("0000000000 65535 f \n")
	for i := 1; i <= totalObjs; i++ {
		fmt.Fprintf(&pdf, "%010d 00000 n \n", offsets[i])
	}

	// Fix 6.1.3: /ID array is mandatory in PDF/A-1b. Both entries are identical
	// for a freshly created document (no previous revision). The hash is derived
	// from content length + generation time so each report gets a unique ID.
	idInput := fmt.Sprintf("%d-%s", pdf.Len(), time.Now().UTC().Format(time.RFC3339Nano))
	idHash := md5.Sum([]byte(idInput))
	idHex := fmt.Sprintf("%X", idHash)

	fmt.Fprintf(&pdf,
		"trailer\n<</Size %d /Root 1 0 R /ID [<%s> <%s>]>>\nstartxref\n%d\n%%%%EOF\n",
		totalObjs+1, idHex, idHex, xrefOffset,
	)

	return pdf.Bytes(), nil
}

// winAnsiEncode maps a Unicode rune to its Windows-1252 (WinAnsiEncoding) byte.
// Returns (byte, true) if the rune is representable, (0, false) if not.
// Printable ASCII (U+0020–U+007E) and Latin-1 supplement (U+00A0–U+00FF) map
// directly. The 27 special chars in the 0x80–0x9F block are handled explicitly;
// the five undefined Windows-1252 slots (0x81, 0x8D, 0x8F, 0x90, 0x9D) are not
// reachable and are never emitted into the PDF stream.
func winAnsiEncode(r rune) (byte, bool) {
	if r >= 0x20 && r <= 0x7E {
		return byte(r), true
	}
	if r >= 0xA0 && r <= 0xFF {
		return byte(r), true
	}
	switch r {
	case 0x20AC:
		return 0x80, true // €
	case 0x201A:
		return 0x82, true // ‚
	case 0x0192:
		return 0x83, true // ƒ
	case 0x201E:
		return 0x84, true // „
	case 0x2026:
		return 0x85, true // …
	case 0x2020:
		return 0x86, true // †
	case 0x2021:
		return 0x87, true // ‡
	case 0x02C6:
		return 0x88, true // ˆ
	case 0x2030:
		return 0x89, true // ‰
	case 0x0160:
		return 0x8A, true // Š
	case 0x2039:
		return 0x8B, true // ‹
	case 0x0152:
		return 0x8C, true // Œ
	case 0x017D:
		return 0x8E, true // Ž
	case 0x2018:
		return 0x91, true // '
	case 0x2019:
		return 0x92, true // '
	case 0x201C:
		return 0x93, true // "
	case 0x201D:
		return 0x94, true // "
	case 0x2022:
		return 0x95, true // •
	case 0x2013:
		return 0x96, true // –
	case 0x2014:
		return 0x97, true // —
	case 0x02DC:
		return 0x98, true // ˜
	case 0x2122:
		return 0x99, true // ™
	case 0x0161:
		return 0x9A, true // š
	case 0x203A:
		return 0x9B, true // ›
	case 0x0153:
		return 0x9C, true // œ
	case 0x017E:
		return 0x9E, true // ž
	case 0x0178:
		return 0x9F, true // Ÿ
	}
	return 0, false
}

func statusLabel(s string) string {
	switch s {
	case "SUCCESS":
		return "PASSED - The workload demonstrated expected resilience characteristics."
	case "FAILED":
		return "FAILED - The workload did not meet resilience criteria. See log for details."
	case "CANCELLED":
		return "CANCELLED - The test run was terminated before completion."
	default:
		return s
	}
}

func finishedAtStr(r database.TestRun) string {
	if r.FinishedAt != nil {
		return r.FinishedAt.Format("2006-01-02 15:04:05 UTC")
	}
	return "-"
}
