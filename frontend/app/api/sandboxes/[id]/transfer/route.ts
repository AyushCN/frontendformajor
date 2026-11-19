import { NextRequest } from "next/server";
import { POST as transferPost } from "@/app/api/environments/[id]/transfer/route";

export async function POST(
  req: NextRequest,
  props: { params: Promise<{ id: string }> }
) {
  return transferPost(req, props);
}
