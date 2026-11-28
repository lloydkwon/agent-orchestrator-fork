import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { TERMINAL_FONT_SIZE_DEFAULT, TERMINAL_LINE_HEIGHT } from "./design-tokens";
import { buildTerminalThemes } from "./terminal-themes";
import { THEMES, type ThemePalette } from "./theme-palettes";
import { deriveThemeTokens, installThemeStylesheet } from "./theme-tokens";

// dotfiles ghostty/themes/terminal-look-{dark,light} 의 값. kitty 와 다르면 ghostty 를 따른다.
const LOOK = {
	dark: {
		background: "#1e1e1e", foreground: "#cccccc", cursor: "#aeafad", cursorAccent: "#111111", selection: "#264f78",
		ansi: [
			"#000000", "#cc6666", "#b5bd68", "#f0c674", "#81a2be", "#b294bb", "#8abeb7", "#c5c8c6",
			"#969896", "#cc6666", "#b5bd68", "#f0c674", "#81a2be", "#b294bb", "#8abeb7", "#e0e0e0",
		],
	},
	light: {
		background: "#ffffff", foreground: "#24292e", cursor: "#044289", cursorAccent: "#ffffff", selection: "#dae9f9",
		ansi: [
			"#24292e", "#d73a49", "#1a7f37", "#9a6700", "#0366d6", "#5a32a3", "#1b7c83", "#6a737d",
			"#6e7781", "#cb2431", "#22863a", "#b08800", "#005cc5", "#5a32a3", "#3192aa", "#d1d5da",
		],
	},
} as const;

const SLOTS = ["black", "red", "green", "yellow", "blue", "magenta", "cyan", "white"] as const;
const TOKEN_SUFFIXES = [...SLOTS, ...SLOTS.map((slot) => `bright-${slot}`)];
const XTERM_KEYS = [...SLOTS, ...SLOTS.map((slot) => `bright${slot[0].toUpperCase()}${slot.slice(1)}`)];

function terminalLook(mode: "dark" | "light"): ThemePalette {
	const theme = THEMES.find((entry) => (entry.id as string) === "terminal-look");
	if (!theme) throw new Error("terminal-look theme is missing from the catalog");
	return theme[mode];
}

describe("terminal-look theme", () => {
	it.each(["dark", "light"] as const)("%s derives the exact 16 colors and selection", (mode) => {
		const tokens = deriveThemeTokens(terminalLook(mode), mode);
		expect(TOKEN_SUFFIXES.map((suffix) => tokens[`--color-term-${suffix}`])).toEqual([...LOOK[mode].ansi]);
		expect(tokens[`--color-term-selection-${mode}`]).toBe(LOOK[mode].selection);
		expect(tokens["--background"]).toBe(LOOK[mode].background);
		expect(tokens["--foreground"]).toBe(LOOK[mode].foreground);
	});

	describe("xterm palette", () => {
		const root = document.documentElement;
		beforeEach(() => installThemeStylesheet());
		afterEach(() => {
			delete root.dataset.styleTheme;
			delete root.dataset.theme;
		});

		it.each(["dark", "light"] as const)("%s keeps ghostty's cursor, selection and true black", (mode) => {
			root.dataset.styleTheme = "terminal-look";
			root.dataset.theme = mode;
			const theme = buildTerminalThemes()[mode] as Record<string, string>;
			expect(theme).toMatchObject({
				background: LOOK[mode].background,
				foreground: LOOK[mode].foreground,
				cursor: LOOK[mode].cursor,
				cursorAccent: LOOK[mode].cursorAccent,
				selectionBackground: LOOK[mode].selection,
				selectionInactiveBackground: LOOK[mode].selection,
			});
			expect(XTERM_KEYS.map((key) => theme[key])).toEqual([...LOOK[mode].ansi]);
		});

		it.each(["dark", "light"] as const)("%s leaves other named themes collapsing black into the plate", (mode) => {
			root.dataset.styleTheme = "github";
			root.dataset.theme = mode;
			const theme = buildTerminalThemes()[mode];
			expect(theme.black).toBe(theme.background);
			expect(theme.cursorAccent).toBe(theme.background);
		});
	});
});

describe("terminal font tokens", () => {
	const css = readFileSync(resolve(__dirname, "../../styles/tokens.css"), "utf8");

	// Ghostty maps the Nerd Font ranges to Monaco and 941 symbols to the last four
	// faces (font-codepoint-map). This order picks the same face for all of them but
	// U+26A1.
	it("uses the ghostty faces on Linux only, ending in the bundled stack", () => {
		const linux = css.match(/:root\[data-ao-platform="linux"\]\s*\{([^}]*)\}/)?.[1] ?? "";
		const value = linux.match(/--font-terminal:\s*([^;]*);/)?.[1].replace(/\s+/g, " ").trim();
		expect(value).toBe(
			'"Cascadia Code", "Monaco Nerd Font", "D2Coding Ghostty", "D2Coding DemiLight", "D2Coding", "Noto Sans CJK KR", "Noto Sans Symbols2", "Noto Sans Math", "Noto Sans Symbols", "FreeMono", var(--font-family-mono)',
		);
		expect(css).toMatch(/--font-terminal:\s*var\(--font-family-mono\);/);
		expect(css.match(/"Cascadia Code"/g)).toHaveLength(1);
	});

	it("gives a 14px terminal a 19px cell (16px glyph box x 1.2, floored by xterm)", () => {
		expect(TERMINAL_FONT_SIZE_DEFAULT).toBe(14);
		expect(Math.floor(16 * TERMINAL_LINE_HEIGHT)).toBe(19);
	});
});
