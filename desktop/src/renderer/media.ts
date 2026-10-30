import type { Message } from "../gen/backend/v1/types_pb";
import { mediaURL } from "../shared/media";

/**
 * attachmentURL is m's attachment as the window loads it (kith-media:), its version all
 * the message says of what the attachment is now: the edit it shows and the attachment's
 * own description. An edit that replaces a picture is a new URL, so neither main's cache
 * nor the browser's shows the old one.
 */
export function attachmentURL(m: Message): string {
  const a = m.media;
  const version = [m.revisionId, a?.type, a?.name, a?.mime, a?.size, a?.width, a?.height].map(String).join("\n");
  return mediaURL(m.roomId, m.id, version);
}
