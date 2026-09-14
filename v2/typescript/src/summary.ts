/**
 * The ONE rule the three SDKs use to cut a record summary.
 *
 * Its own file because `memory.ts` is long past the ~300-line house cut and
 * house law forbids growing a file already over it. This is a real domain: a
 * cross-SDK wire invariant that must be changed in three places at once, not a
 * private detail of the Memory class.
 *
 * @module
 */

/** Maximum summary length, in Unicode CODE POINTS (not UTF-16 code units). */
export const SUMMARY_MAX_CODE_POINTS = 200;

/**
 * Truncate `text` to 200 Unicode code points, with an ellipsis when cut.
 *
 * Junior Tip [why `Array.from` and not `text.slice(0, 200)`]: `slice` counts
 * UTF-16 CODE UNITS, so an astral character (emoji, CJK extension B, most
 * mathematical alphanumerics) is two units and can be split down the middle,
 * emitting a lone surrogate — an unpaired half-character that is not valid
 * text and that a strict JSON or DB layer downstream may reject. `Array.from`
 * iterates by code point, so Python's `str[:200]`, Go's `[]rune` and this
 * truncate at the SAME character. That agreement is the point: the summary
 * column is what search and the manifest display, and three SDKs cutting the
 * same record at three different offsets would make "the same write" produce
 * three different rows.
 *
 * @param text - Full record text.
 * @returns `text` unchanged, or its first 200 code points plus `"..."`.
 */
export function truncateSummary(text: string): string {
  const codePoints = Array.from(text);
  return codePoints.length > SUMMARY_MAX_CODE_POINTS
    ? codePoints.slice(0, SUMMARY_MAX_CODE_POINTS).join("") + "..."
    : text;
}
