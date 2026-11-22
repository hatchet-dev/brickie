import { docsLlms } from "@/source";

export const revalidate = false;

export async function GET() {
  return (await docsLlms.serve("/llms-full.txt"))!;
}
