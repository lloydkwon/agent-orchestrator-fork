// A ruler of operators would become one glyph as wide as the line and fill the
// texture atlas, so long runs stay unjoined.
const MAX_RUN = 16;

const OPERATORS = "!#$%&*+\\-./:<=>?@\\\\^|~_";
const RUN = new RegExp(`(?<![${OPERATORS}])[${OPERATORS}]{2,${MAX_RUN}}(?![${OPERATORS}])`, "g");

/**
 * LADO fork: cell ranges to draw as one string so the font can shape them
 * (`->`, `!=`), as Ghostty does. Which of them become ligatures is the font's
 * call; the rest look the same as drawn apart.
 */
export function ligatureRanges(text: string): [number, number][] {
	const ranges: [number, number][] = [];
	for (const match of text.matchAll(RUN)) {
		// A joined cell loses the extended attributes that draw a link's dotted
		// underline, and this run sits in nearly every link.
		if (match[0] === "://") continue;
		ranges.push([match.index, match.index + match[0].length]);
	}
	return ranges;
}

export type LigatureMetrics = {
	/** Canvas font shorthand of the terminal's regular face. */
	font: string;
	cellWidth: number;
	devicePixelRatio: number;
	/** Joined cells share one highlight, so search results would bleed. */
	paused: boolean;
};

const SAMPLE = "0".repeat(16);

/** Width of the sample in CSS pixels, or null when it cannot be measured. */
function measureSample(font: string): number | null {
	const ctx = document.createElement("canvas").getContext("2d");
	if (!ctx) return null;
	ctx.font = font;
	return ctx.measureText(SAMPLE).width;
}

/**
 * xterm draws a joined run with one fillText, so every fraction of a pixel
 * between the glyph and the cell adds up along it. Join only where they are
 * equal: full hinting makes them so (dotfiles fontconfig, for this app), a
 * scaled window or another font size may not.
 */
export function createLigatureJoiner(
	read: () => LigatureMetrics,
	measure: (font: string) => number | null = measureSample,
): (text: string) => [number, number][] {
	let measuredFont = "";
	let measuredWidth: number | null = null;
	return (text) => {
		const { font, cellWidth, devicePixelRatio, paused } = read();
		if (paused || devicePixelRatio !== 1 || cellWidth <= 0) return [];
		if (font !== measuredFont) {
			measuredFont = font;
			measuredWidth = measure(font);
		}
		if (measuredWidth === null || Math.abs(measuredWidth - cellWidth * SAMPLE.length) > 0.01) return [];
		return ligatureRanges(text);
	};
}
