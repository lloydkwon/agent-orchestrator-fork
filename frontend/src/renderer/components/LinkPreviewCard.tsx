import { useState } from "react";
import { Globe } from "lucide-react";
import { cn } from "../lib/utils";
import { openLinkInSystemBrowser } from "../lib/external-link-policy";
import { ladoWebLinkClickOpensAOBrowser } from "../lib/lado-link-click";
import type { LinkPreview } from "../hooks/useLinkPreview";
import { Skeleton } from "./ui/skeleton";

function linkDomain(url: string): string {
	try {
		return new URL(url).hostname.replace(/^www\./, "");
	} catch {
		return url;
	}
}

function PreviewImage({ src, alt, className }: { src: string; alt: string; className?: string }) {
	const [failed, setFailed] = useState(false);
	if (failed) return null;
	return (
		<img
			src={src}
			alt={alt}
			loading="lazy"
			referrerPolicy="no-referrer"
			onError={() => setFailed(true)}
			className={className}
		/>
	);
}

export function LinkPreviewCardLoading() {
	return (
		<div data-testid="link-preview-loading" className="space-y-2 p-3" aria-hidden="true">
			<div className="flex items-center gap-2">
				<Skeleton className="size-4 shrink-0 rounded-sm" />
				<Skeleton className="h-3 w-24" />
			</div>
			<Skeleton className="h-4 w-4/5" />
			<Skeleton className="h-3 w-full" />
			<Skeleton className="h-3 w-3/5" />
		</div>
	);
}

export function LinkPreviewCard({ url, preview, onBrowserOpen, ladoHostLocalOnRemoteHost }: {
	url: string;
	preview: LinkPreview;
	onBrowserOpen?: (url: string) => void;
	/** LADO fork (TDD §11): 원격 호스트의 localhost 링크라 이 컴퓨터의 브라우저로 열지 않는다. */
	ladoHostLocalOnRemoteHost?: boolean;
}) {
	const domain = linkDomain(preview.url || url);
	const siteLabel = preview.siteName || domain;
	const linkProps = {
		href: url,
		onClick: (event: React.MouseEvent<HTMLAnchorElement>) => {
			event.preventDefault();
			// LADO fork (TDD §11): 그냥 클릭은 시스템 브라우저로 연다.
			const hostLocalOnRemote = Boolean(ladoHostLocalOnRemoteHost);
			if (onBrowserOpen && ladoWebLinkClickOpensAOBrowser(event.ctrlKey || event.metaKey || event.altKey, hostLocalOnRemote)) onBrowserOpen(url);
			else if (!hostLocalOnRemote) void openLinkInSystemBrowser(url);
		},
	};
	return (
		<div data-testid="link-preview-card" className="flex flex-col">
			{preview.imageUrl && (
				<PreviewImage
					src={preview.imageUrl}
					alt=""
					className="max-h-36 w-full rounded-t-lg object-cover"
				/>
			)}
			<div className="space-y-1.5 p-3">
				<div className="flex items-center gap-1.5 text-xs text-muted-foreground">
					{preview.faviconUrl ? (
						<PreviewImage src={preview.faviconUrl} alt="" className="size-3.5 shrink-0 rounded-sm" />
					) : (
						<Globe aria-hidden="true" className="size-3.5 shrink-0" />
					)}
					<span className="truncate">{siteLabel}</span>
				</div>
				{preview.title && (
					<a {...linkProps} className={cn("line-clamp-2 text-sm font-medium leading-snug text-popover-foreground hover:underline focus-visible:underline")}>
						{preview.title}
					</a>
				)}
				{preview.description && (
					<div className="line-clamp-2 text-xs text-muted-foreground">{preview.description}</div>
				)}
				<a {...linkProps} className="block truncate text-[11px] text-muted-foreground/70 hover:underline focus-visible:underline">{domain}</a>
			</div>
		</div>
	);
}
