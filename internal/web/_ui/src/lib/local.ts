// How this browser is arranged is this browser's business: panel widths, what
// is open, an unsent draft. None of it goes to the server -- the Registry is
// the shared truth, and someone else's window should not move because this one
// was resized. localStorage can be missing or throw (private windows, blocked
// site data), so every access is guarded and the page works the same without.

const prefix = "projmux.web.";

export function load<T>(key: string, fallback: T): T {
  try {
    const raw = localStorage.getItem(prefix + key);
    return raw === null ? fallback : (JSON.parse(raw) as T);
  } catch {
    return fallback;
  }
}

export function save(key: string, value: unknown): void {
  try {
    localStorage.setItem(prefix + key, JSON.stringify(value));
  } catch {
    /* the page works without it */
  }
}

export function drop(key: string): void {
  try {
    localStorage.removeItem(prefix + key);
  } catch {
    /* the page works without it */
  }
}
