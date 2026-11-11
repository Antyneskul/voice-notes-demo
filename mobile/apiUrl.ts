// The Go backend listens on this port (its PORT setting).
const BACKEND_PORT = 8080;

// A host we can reach the backend on: an IPv4 address, localhost, or a .local name.
// Tunnel hosts (exp.direct, ngrok) point at Expo's servers, not at this computer.
// Other LAN-style names (.lan, Tailscale, .fritz.box) and IPv6 literals are deliberately not
// guessed at: they fall back to localhost, and the field in the app can be edited.
const isLocalHost = (h: string) => {
  const host = h.toLowerCase();
  return host === 'localhost' || host.endsWith('.local') || /^\d{1,3}(\.\d{1,3}){3}$/.test(host);
};

// Where the app should send uploads.
//  1. An explicit URL (EXPO_PUBLIC_API_URL) always wins; a missing http:// is added.
//  2. Otherwise reuse the host the app was loaded from. Expo's dev server and the backend
//     run on the same computer, so when your IP changes you restart Expo, scan the new QR
//     code, and the app follows with nothing to edit.
//  3. Otherwise localhost (the iOS Simulator, or a tunnel the app can't derive anything from).
export function resolveApiUrl({ envUrl, hostUri }: { envUrl?: string; hostUri?: string | null }): string {
  const explicit = envUrl?.trim().replace(/\/+$/, '');
  if (explicit) return /^https?:\/\//i.test(explicit) ? explicit : `http://${explicit}`;

  const host = hostUri?.split('/')[0].split(':')[0].trim(); // "192.168.1.5:8081/path" -> "192.168.1.5"
  if (host && isLocalHost(host)) return `http://${host}:${BACKEND_PORT}`;

  return `http://localhost:${BACKEND_PORT}`;
}
