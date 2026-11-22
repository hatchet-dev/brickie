import { createMDX } from "fumadocs-mdx/next";

const basePath = "/brickie";

const withMDX = createMDX();

export default withMDX({
  output: "export",
  basePath,
  images: { unoptimized: true },
  pageExtensions: ["ts", "tsx"],
  env: { NEXT_PUBLIC_BASE_PATH: basePath },
});
