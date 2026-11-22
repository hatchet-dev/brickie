"use client";

import { useEffect } from "react";

type Repo = "hatchet" | "brickie";

const SWAPS: { repo: Repo; guard: string; pattern: RegExp; replace: (tag: string) => string }[] = [
  {
    repo: "hatchet",
    guard: "ghcr.io/hatchet-dev/hatchet/",
    pattern: /(ghcr\.io\/hatchet-dev\/hatchet\/hatchet-[a-z-]+:)latest\b/g,
    replace: (tag) => `$1${tag}`,
  },
  {
    repo: "brickie",
    guard: "brickie_X.Y.Z",
    pattern: /brickie_X\.Y\.Z/g,
    replace: (tag) => `brickie_${tag.replace(/^v/, "")}`,
  },
  {
    repo: "brickie",
    guard: "vX.Y.Z",
    pattern: /\bvX\.Y\.Z\b/g,
    replace: (tag) => tag,
  },
];

const latest = new Map<Repo, Promise<string | undefined>>();

function latestTag(repo: Repo) {
  let pending = latest.get(repo);
  if (!pending) {
    pending = fetch(`https://api.github.com/repos/hatchet-dev/${repo}/releases/latest`, {
      headers: { Accept: "application/vnd.github+json" },
    })
      .then((res) => (res.ok ? res.json() : undefined))
      .then((release) => (/^v\d+\.\d+\.\d+(-\d+)?$/.test(release?.tag_name) ? (release.tag_name as string) : undefined))
      .catch(() => undefined)
      .then((tag) => {
        if (!tag) latest.delete(repo);
        return tag;
      });
    latest.set(repo, pending);
  }
  return pending;
}

function swapTags(root: Node, tags: Partial<Record<Repo, string>>) {
  const swap = (node: Node) => {
    let value = node.nodeValue;
    if (!value) return;
    for (const { repo, guard, pattern, replace } of SWAPS) {
      const tag = tags[repo];
      if (!tag || !value.includes(guard)) continue;
      value = value.replace(pattern, replace(tag));
    }
    if (value !== node.nodeValue) node.nodeValue = value;
  };
  if (root.nodeType === Node.TEXT_NODE) {
    swap(root);
    return;
  }
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  for (let node = walker.nextNode(); node; node = walker.nextNode()) swap(node);
}

export function LatestTags() {
  useEffect(() => {
    let cancelled = false;
    let observer: MutationObserver | undefined;
    Promise.all([latestTag("hatchet"), latestTag("brickie")]).then(([hatchet, brickie]) => {
      if (cancelled || (!hatchet && !brickie)) return;
      const tags = { hatchet, brickie };
      swapTags(document.body, tags);
      observer = new MutationObserver((mutations) => {
        for (const m of mutations) {
          if (m.type === "characterData") swapTags(m.target, tags);
          else m.addedNodes.forEach((node) => swapTags(node, tags));
        }
      });
      observer.observe(document.body, { subtree: true, childList: true, characterData: true });
    });
    return () => {
      cancelled = true;
      observer?.disconnect();
    };
  }, []);
  return null;
}
