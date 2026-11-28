import type { WebglAddon } from "@xterm/addon-webgl";
import { lightness } from "./theme-color";

/**
 * LADO fork: draw terminal glyphs the way the owner's Ghostty does.
 *
 * Ghostty's dark theme blends text linearly, which lifts every half-covered
 * pixel; Chromium blends in sRGB, so the same glyph comes out thinner. Drawing
 * a glyph a second time at part strength raises the half-covered pixels and
 * leaves full and empty ones alone. (Dim text is a translucent color, so there
 * the full ones rise too, as they do in Ghostty.) Ghostty's light theme blends close to sRGB
 * and needs none of it. Measured against Ghostty over 20 sample lines (mean
 * channel difference of the Latin line): dark 5.1 -> 1.8 at 0.7, light 1.4
 * untouched. Text darker than its own cell (reverse video) gets none either:
 * linear blending thins it.
 *
 * xterm also rounds the spare half pixel of an odd line gap down the cell while
 * Ghostty rounds it up, so every glyph sits one pixel low.
 *
 * Neither has an option, so this wraps the drawing context of xterm's texture
 * atlas. It touches private fields: when they are not what it expects, glyphs
 * simply keep xterm's own look.
 */
const DARK_SECOND_PASS = 0.7;

type AtlasContext = Pick<
	CanvasRenderingContext2D,
	"fillText" | "fillRect" | "fillStyle" | "globalAlpha" | "globalCompositeOperation"
>;

type Atlas = {
	_tmpCtx?: AtlasContext;
	_config?: {
		deviceCellHeight?: number;
		deviceCharHeight?: number;
		devicePixelRatio?: number;
		colors?: { background?: { css?: string } };
	};
};

type WebglInternals = {
	_renderer?: { _charAtlas?: Atlas };
	clearTextureAtlas?: () => void;
	onChangeTextureAtlas?: (listener: () => void) => unknown;
};

// Terminals that share a configuration share one atlas.
const wrapped = new WeakSet<AtlasContext>();

/** Lightness of a color as a canvas reports it: `#rrggbb`, or `rgba()` with alpha. */
function cssLightness(css: unknown): number | null {
	if (typeof css !== "string") return null;
	const hex = /^#[0-9a-f]{6}/i.exec(css)?.[0];
	if (hex) return lightness(hex);
	const channels = /^rgba?\((\d+)[, ]+(\d+)[, ]+(\d+)/.exec(css)?.slice(1, 4);
	if (!channels) return null;
	return lightness(`#${channels.map((channel) => Number(channel).toString(16).padStart(2, "0")).join("")}`);
}

function secondPass(background: string | undefined): number {
	const plate = cssLightness(background);
	return plate !== null && plate < 0.5 ? DARK_SECOND_PASS : 0;
}

function wrapAtlas(atlas: Atlas | undefined): boolean {
	const ctx = atlas?._tmpCtx;
	const config = atlas?._config;
	if (!ctx || !config || wrapped.has(ctx) || config.devicePixelRatio !== 1) return false;
	const spare = (config.deviceCellHeight ?? 0) - (config.deviceCharHeight ?? 0);
	const lift = spare > 0 && spare % 2 === 1 ? 1 : 0;
	const strength = secondPass(config.colors?.background?.css);
	if (!lift && !strength) return false;
	const fillText = ctx.fillText.bind(ctx);
	const fillRect = ctx.fillRect.bind(ctx);
	let cell = cssLightness(config.colors?.background?.css) ?? 0;
	ctx.fillRect = (x, y, width, height) => {
		// The glyph's own cell: xterm clears the scratch canvas with its color.
		if (ctx.globalCompositeOperation === "copy") cell = cssLightness(ctx.fillStyle) ?? cell;
		fillRect(x, y, width, height);
	};
	ctx.fillText = (text, x, y) => {
		fillText(text, x, y - lift);
		if (!strength || (cssLightness(ctx.fillStyle) ?? 1) <= cell) return;
		const alpha = ctx.globalAlpha;
		ctx.globalAlpha = alpha * strength;
		fillText(text, x, y - lift);
		ctx.globalAlpha = alpha;
	};
	wrapped.add(ctx);
	return true;
}

/** Call once the addon is loaded; later atlases (theme, font size) are followed. */
export function matchGhosttyGlyphs(addon: WebglAddon): void {
	if (document.documentElement.dataset.aoPlatform !== "linux") return;
	const internals = addon as unknown as WebglInternals;
	const apply = () => {
		try {
			if (wrapAtlas(internals._renderer?._charAtlas)) internals.clearTextureAtlas?.();
		} catch (error) {
			console.warn("xterm: could not match Ghostty glyphs", error);
		}
	};
	apply();
	// The caller gives up on WebGL for good when attaching throws.
	try {
		// xterm fires this just before it stores the new atlas.
		internals.onChangeTextureAtlas?.(() => queueMicrotask(apply));
	} catch (error) {
		console.warn("xterm: could not follow the texture atlas", error);
	}
}
