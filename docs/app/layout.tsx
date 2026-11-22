import "@/styles/global.css";
import SearchDialog from "@/components/search";
import { RootProvider } from "fumadocs-ui/provider/next";
import type { Metadata } from "next";
import { DM_Mono, Inter } from "next/font/google";
import type { ReactNode } from "react";

const inter = Inter({ subsets: ["latin"], variable: "--font-inter" });
const dmMono = DM_Mono({ subsets: ["latin"], weight: ["400", "500"], variable: "--font-dm-mono" });

export const metadata: Metadata = {
  metadataBase: new URL("https://hatchet-dev.github.io/brickie"),
  title: { template: "%s - Brickie", default: "Brickie" },
  description: "Agent sandboxes as Hatchet tasks, in your own cloud account.",
};

export default function Layout({ children }: { children: ReactNode }) {
  return (
    <html lang="en" className={`${inter.variable} ${dmMono.variable}`} suppressHydrationWarning>
      <body className="flex min-h-screen flex-col">
        <RootProvider theme={{ defaultTheme: "dark" }} search={{ SearchDialog }}>
          {children}
        </RootProvider>
      </body>
    </html>
  );
}
