import { docs } from "@/.source/server";
import { llms, loader } from "fumadocs-core/source";
import { icons } from "lucide-react";
import { createElement } from "react";

export const site = "https://hatchet-dev.github.io/brickie";

export const source = loader({
  baseUrl: "/",
  source: docs.toFumadocsSource(),
  icon(name) {
    if (name && name in icons) {
      return createElement(icons[name as keyof typeof icons], { className: "size-4", strokeWidth: 1.5 });
    }
  },
});

export function markdownUrl(pageUrl: string) {
  return `${site}/llms.mdx${pageUrl === "/" ? "/index" : pageUrl}.md`;
}

export const docsLlms = llms(source, {
  renderUrl: (item) => (/\.(md|txt)$/.test(item.url) ? `${site}/llms.mdx${item.url}` : `${site}${item.url}`),
  async renderPage(page) {
    const body = (await page.data.getText("processed"))
      .replace(/^<(SandboxSession|TurnLifecycle|WarmPool) \/>\n\n?/gm, "")
      .replace(/<Callout[^>]*title="([^"]*)"[^>]*>\s*([\s\S]*?)\s*<\/Callout>/g, (_, title, text) => `> **${title}** ${text.trim()}`)
      .replace(/<Callout[^>]*>\s*([\s\S]*?)\s*<\/Callout>/g, (_, text) => `> ${text.trim()}`)
      .replace(/<Card href="([^"]*)" title="([^"]*)" description="([^"]*)" \/>/g, (_, href, title, desc) => `- [${title}](${site}${href}): ${desc}`)
      .replace(/^<\/?(Cards|Steps|Step)>\n?/gm, "");
    return `# ${page.data.title}\n\n${page.data.description ?? ""}\n\n${body}`;
  },
});
