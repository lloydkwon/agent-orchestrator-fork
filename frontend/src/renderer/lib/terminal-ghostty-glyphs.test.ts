import type { WebglAddon } from "@xterm/addon-webgl";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { matchGhosttyGlyphs } from "./terminal-ghostty-glyphs";

type Drawn = { text: string; y: number; alpha: number };

function fakeAtlas(background: string, cell = 19, char = 16) {
	const drawn: Drawn[] = [];
	const ctx = {
		globalAlpha: 1,
		globalCompositeOperation: "source-over",
		fillStyle: "#cccccc",
		fillRect: vi.fn(),
		fillText(text: string, _x: number, y: number) {
			drawn.push({ text, y, alpha: ctx.globalAlpha });
		},
	};
	return {
		drawn,
		ctx,
		atlas: {
			_tmpCtx: ctx,
			_config: { deviceCellHeight: cell, deviceCharHeight: char, devicePixelRatio: 1, colors: { background: { css: background } } },
		},
	};
}

function fakeAddon(atlas: unknown) {
	const listeners: Array<() => void> = [];
	const addon = {
		_renderer: { _charAtlas: atlas },
		clearTextureAtlas: vi.fn(),
		onChangeTextureAtlas: (listener: () => void) => {
			listeners.push(listener);
			return { dispose: () => undefined };
		},
	};
	return { addon: addon as unknown as WebglAddon, raw: addon, change: () => listeners.forEach((listener) => listener()) };
}

describe("matchGhosttyGlyphs", () => {
	beforeEach(() => {
		document.documentElement.dataset.aoPlatform = "linux";
	});
	afterEach(() => {
		delete document.documentElement.dataset.aoPlatform;
	});

	it("thickens glyphs on a dark background and lifts them by the rounded half pixel", () => {
		const { atlas, ctx, drawn } = fakeAtlas("#1e1e1e");
		const { addon, raw } = fakeAddon(atlas);

		matchGhosttyGlyphs(addon);
		ctx.fillText("a", 0, 20);

		expect(drawn).toEqual([
			{ text: "a", y: 19, alpha: 1 },
			{ text: "a", y: 19, alpha: 0.7 },
		]);
		expect(ctx.globalAlpha).toBe(1);
		// Glyphs drawn before the change must be drawn again.
		expect(raw.clearTextureAtlas).toHaveBeenCalledTimes(1);
	});

	it.each([
		["light text on the plate", "#1e1e1e", "#cccccc", 2],
		["reverse video", "#cccccc", "#1e1e1e", 1],
		["dark text on a yellow cell", "#f0c674", "#1e1e1e", 1],
		["light text on a blue cell", "#264f78", "#cccccc", 2],
	])("thickens only text lighter than its own cell: %s", (_name, cell, text, passes) => {
		const { atlas, ctx, drawn } = fakeAtlas("#1e1e1e");
		matchGhosttyGlyphs(fakeAddon(atlas).addon);

		// xterm paints the cell first, then the glyph.
		ctx.globalCompositeOperation = "copy";
		ctx.fillStyle = cell;
		ctx.fillRect(0, 0, 20, 20);
		ctx.globalCompositeOperation = "source-over";
		ctx.fillStyle = text;
		ctx.fillText("a", 0, 20);

		expect(drawn).toHaveLength(passes);
		expect(ctx.fillStyle).toBe(text);
	});

	it("does not thicken reverse video on a light plate", () => {
		const { atlas, ctx, drawn } = fakeAtlas("#ffffff");
		matchGhosttyGlyphs(fakeAddon(atlas).addon);
		ctx.globalCompositeOperation = "copy";
		ctx.fillStyle = "#24292e";
		ctx.fillRect(0, 0, 20, 20);
		ctx.globalCompositeOperation = "source-over";
		ctx.fillStyle = "#ffffff";
		ctx.fillText("a", 0, 20);

		expect(drawn).toHaveLength(1);
	});

	it("does not take a drawn shape for the cell", () => {
		const { atlas, ctx, drawn } = fakeAtlas("#1e1e1e");
		matchGhosttyGlyphs(fakeAddon(atlas).addon);
		ctx.fillStyle = "#ffffff";
		ctx.fillRect(0, 0, 4, 4);
		ctx.fillStyle = "#cccccc";
		ctx.fillText("a", 0, 20);

		expect(drawn).toHaveLength(2);
		expect(ctx.globalCompositeOperation).toBe("source-over");
	});

	it("reads the rgba() a dim color becomes", () => {
		const { atlas, ctx, drawn } = fakeAtlas("#1e1e1e");
		matchGhosttyGlyphs(fakeAddon(atlas).addon);
		ctx.fillStyle = "rgba(204, 204, 204, 0.5)";
		ctx.fillText("a", 0, 20);

		expect(drawn).toHaveLength(2);
	});

	it("keeps dim text dim", () => {
		const { atlas, ctx, drawn } = fakeAtlas("#1e1e1e");
		matchGhosttyGlyphs(fakeAddon(atlas).addon);
		ctx.globalAlpha = 0.5;
		ctx.fillText("a", 0, 20);

		expect(drawn.map(({ alpha }) => alpha)).toEqual([0.5, 0.35]);
		expect(ctx.globalAlpha).toBe(0.5);
	});

	it("only lifts glyphs on a light background", () => {
		const { atlas, ctx, drawn } = fakeAtlas("#ffffff");
		matchGhosttyGlyphs(fakeAddon(atlas).addon);
		ctx.fillText("a", 0, 20);

		expect(drawn).toEqual([{ text: "a", y: 19, alpha: 1 }]);
	});

	it("does not move glyphs when the spare rows split evenly", () => {
		const { atlas, ctx, drawn } = fakeAtlas("#ffffff", 18, 16);
		matchGhosttyGlyphs(fakeAddon(atlas).addon);
		ctx.fillText("a", 0, 20);

		expect(drawn).toEqual([{ text: "a", y: 20, alpha: 1 }]);
	});

	it("leaves scaled displays alone", () => {
		const { atlas, ctx, drawn } = fakeAtlas("#1e1e1e");
		atlas._config.devicePixelRatio = 2;
		matchGhosttyGlyphs(fakeAddon(atlas).addon);
		ctx.fillText("a", 0, 20);

		expect(drawn).toEqual([{ text: "a", y: 20, alpha: 1 }]);
	});

	it("wraps an atlas shared by two terminals once", () => {
		const { atlas, ctx, drawn } = fakeAtlas("#1e1e1e");
		matchGhosttyGlyphs(fakeAddon(atlas).addon);
		const second = fakeAddon(atlas);
		matchGhosttyGlyphs(second.addon);
		ctx.fillText("a", 0, 20);

		expect(drawn).toHaveLength(2);
		expect(second.raw.clearTextureAtlas).not.toHaveBeenCalled();
	});

	it("follows the atlas a theme change brings in", async () => {
		const dark = fakeAtlas("#1e1e1e");
		const light = fakeAtlas("#ffffff");
		const { addon, raw, change } = fakeAddon(dark.atlas);
		matchGhosttyGlyphs(addon);

		// xterm announces the change before it stores the new atlas.
		change();
		raw._renderer._charAtlas = light.atlas;
		await Promise.resolve();
		light.ctx.fillText("a", 0, 20);

		expect(light.drawn).toEqual([{ text: "a", y: 19, alpha: 1 }]);
	});

	it.each([["no renderer", {}], ["no atlas", { _renderer: {} }], ["no context", { _renderer: { _charAtlas: {} } }]])(
		"does nothing when xterm's internals differ: %s",
		(_name, shape) => {
			expect(() => matchGhosttyGlyphs(shape as unknown as WebglAddon)).not.toThrow();
		},
	);

	it("never lets xterm's internals turn WebGL off for good", () => {
		const { atlas } = fakeAtlas("#1e1e1e");
		const broken = { _renderer: { _charAtlas: atlas }, clearTextureAtlas: vi.fn(), onChangeTextureAtlas: "not a function" };
		const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);

		expect(() => matchGhosttyGlyphs(broken as unknown as WebglAddon)).not.toThrow();
		expect(warn).toHaveBeenCalledOnce();
		warn.mockRestore();
	});

	it("is Linux only", () => {
		delete document.documentElement.dataset.aoPlatform;
		const { atlas, ctx, drawn } = fakeAtlas("#1e1e1e");
		matchGhosttyGlyphs(fakeAddon(atlas).addon);
		ctx.fillText("a", 0, 20);

		expect(drawn).toEqual([{ text: "a", y: 20, alpha: 1 }]);
	});
});
