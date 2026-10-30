import path from "node:path";

/**
 * The window's Content-Security-Policy: its own scripts and styles, pictures from itself,
 * a blob or main (kith-media:, the attachments), and no network at all; kithd is reached
 * through main, never by the page.
 */
export const csp =
  "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' blob: kith-media:; " +
  "media-src kith-media:; connect-src 'none'";

/**
 * The policy under `npm start`, where Vite's dev server injects each stylesheet as a
 * <style> element. A packaged window never gets it.
 */
export const devCSP = csp.replace("style-src 'self'", "style-src 'self' 'unsafe-inline'");

/**
 * appFile is the file under root an app://kith URL names, or undefined for any URL that
 * names something else: another host, or a path that escapes root once decoded.
 */
export function appFile(root: string, url: string): string | undefined {
  let parsed: URL;
  let rel: string;
  try {
    parsed = new URL(url);
    rel = decodeURIComponent(parsed.pathname);
  } catch {
    return undefined;
  }
  if (parsed.protocol !== "app:" || parsed.host !== "kith") {
    return undefined;
  }
  const file = path.resolve(root, "." + path.posix.normalize("/" + rel));
  return file.startsWith(path.resolve(root) + path.sep) ? file : undefined;
}
