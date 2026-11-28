import { describe, expect, it, vi } from "vitest";
import { createLigatureJoiner, ligatureRanges, type LigatureMetrics } from "./terminal-ligatures";

const joined = (text: string) => ligatureRanges(text).map(([start, end]) => text.slice(start, end));

describe("ligatureRanges", () => {
	it("joins operator runs so the font can shape them", () => {
		expect(joined("a -> b != c => d <= e === f || g && h")).toEqual(["->", "!=", "=>", "<=", "===", "||", "&&"]);
		expect(joined("../dir a::b //x ++")).toEqual(["../", "::", "//", "++"]);
	});

	it("leaves single operators, letters and Hangul alone", () => {
		expect(joined("a - b = c / d 한글 -x path/to/file 12:30")).toEqual([]);
	});

	it("leaves rulers alone: one huge glyph per line would fill the atlas", () => {
		expect(joined("-".repeat(16))).toEqual(["-".repeat(16)]);
		expect(joined("-".repeat(17))).toEqual([]);
		expect(joined(`x ${"=".repeat(80)} -> y`)).toEqual(["->"]);
	});

	it("leaves :// apart: a joined cell loses the link's dotted underline", () => {
		expect(joined("see http://host and file:///tmp -> x")).toEqual([":///", "->"]);
	});

	it("starts over on every row", () => {
		expect(joined("a -> b")).toEqual(["->"]);
		expect(joined("-> b")).toEqual(["->"]);
	});

	it("does not join the box drawing xterm draws itself", () => {
		expect(joined("──── │││ ├─┤ ████")).toEqual([]);
	});
});

describe("createLigatureJoiner", () => {
	const metrics = (over: Partial<LigatureMetrics> = {}): LigatureMetrics => ({
		font: "400 14px Cascadia Code",
		cellWidth: 8,
		devicePixelRatio: 1,
		paused: false,
		...over,
	});

	it("joins while a glyph is exactly one cell wide", () => {
		const join = createLigatureJoiner(() => metrics(), () => 8 * 16);
		expect(join("a -> b")).toEqual([[2, 4]]);
	});

	it.each([
		["unhinted advance", metrics(), 8.2 * 16],
		["scaled window", metrics({ devicePixelRatio: 1.095 }), 8 * 16],
		["search open", metrics({ paused: true }), 8 * 16],
		["cell not measured yet", metrics({ cellWidth: 0 }), 8 * 16],
		["no canvas to measure with", metrics(), null],
	])("draws cell by cell otherwise: %s", (_name, value, width) => {
		expect(createLigatureJoiner(() => value, () => width)("a -> b")).toEqual([]);
	});

	it("measures a font once", () => {
		const measure = vi.fn(() => 8 * 16);
		const join = createLigatureJoiner(() => metrics(), measure);
		join("a -> b");
		join("c => d");
		expect(measure).toHaveBeenCalledTimes(1);
	});

	it("measures again when the font size changes", () => {
		let value = metrics();
		const join = createLigatureJoiner(() => value, (font) => (font.includes("14px") ? 8 * 16 : 8.79 * 16));
		expect(join("a -> b")).toHaveLength(1);
		value = metrics({ font: "400 15px Cascadia Code" });
		expect(join("a -> b")).toEqual([]);
	});
});
