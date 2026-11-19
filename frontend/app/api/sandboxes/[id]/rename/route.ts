import { NextRequest } from "next/server";
import { POST as renamePost, PATCH as renamePatch } from "@/app/api/environments/[id]/rename/route";

export async function POST(
  req: NextRequest,
  props: { params: Promise<{ id: string }> }
) {
  return renamePost(req, props);
}

export async function PATCH(
  req: NextRequest,
  props: { params: Promise<{ id: string }> }
) {
  return renamePatch(req, props);
}
