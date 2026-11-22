import { Logo } from "@/components/logo";
import { source } from "@/source";
import { DocsLayout } from "fumadocs-ui/layouts/docs";
import { ThemeSwitch } from "fumadocs-ui/layouts/shared/slots/theme-switch";
import { BookOpen, Package, Ship } from "lucide-react";
import type { ReactNode } from "react";

const GitHubIcon = ({ className }: { className?: string }) => (
  <svg viewBox="0 0 24 24" fill="currentColor" className={className} aria-hidden="true">
    <path d="M12 .3a12 12 0 0 0-3.8 23.4c.6.1.8-.3.8-.6v-2c-3.3.7-4-1.6-4-1.6-.6-1.4-1.4-1.8-1.4-1.8-1.1-.7.1-.7.1-.7 1.2.1 1.8 1.2 1.8 1.2 1.1 1.8 2.8 1.3 3.5 1 .1-.8.4-1.3.8-1.6-2.7-.3-5.5-1.3-5.5-5.9 0-1.3.5-2.4 1.2-3.2-.1-.3-.5-1.5.1-3.2 0 0 1-.3 3.3 1.2a11.5 11.5 0 0 1 6 0c2.3-1.5 3.3-1.2 3.3-1.2.6 1.7.2 2.9.1 3.2.8.8 1.2 1.9 1.2 3.2 0 4.6-2.8 5.6-5.5 5.9.4.4.8 1.1.8 2.2v3.3c0 .3.2.7.8.6A12 12 0 0 0 12 .3Z" />
  </svg>
);

const links = [
  { icon: GitHubIcon, text: "GitHub", url: "https://github.com/hatchet-dev/brickie" },
  { icon: BookOpen, text: "Hatchet docs", url: "https://docs.hatchet.run?utm_source=brickie-docs&utm_medium=sidebar&utm_campaign=brickie-launch&utm_content=footer" },
  { icon: Package, text: "SDK on npm", url: "https://www.npmjs.com/package/@hatchet-dev/sandbox" },
  { icon: Ship, text: "Helm chart", url: "https://github.com/hatchet-dev/hatchet-charts/tree/main/charts/brickie" },
];

export default function Layout({ children }: { children: ReactNode }) {
  return (
    <DocsLayout
      tree={source.pageTree}
      nav={{ title: <Logo />, url: "/" }}
      themeSwitch={{ enabled: false }}
      sidebar={{
        defaultOpenLevel: 1,
        banner: (
          <p className="rounded-lg border border-fd-border bg-fd-card px-3 py-2 text-xs text-fd-muted-foreground">
            Brickie is in beta. Flags, the Helm chart and the wire contract may change before 1.0.
          </p>
        ),
        footer: (
          <div className="flex flex-col gap-1 text-[0.8125rem] text-fd-muted-foreground">
            {links.map(({ icon: Icon, text, url }) => (
              <a
                key={url}
                href={url}
                target="_blank"
                rel="noreferrer noopener"
                className="flex items-center gap-2 rounded-md px-2 py-1 transition-colors hover:bg-fd-accent hover:text-fd-accent-foreground"
              >
                <Icon className="size-4" />
                {text}
              </a>
            ))}
            <ThemeSwitch className="mt-1 self-start" />
          </div>
        ),
      }}
    >
      {children}
    </DocsLayout>
  );
}
