// Small format helpers shared by views.

export function shortSha(sha?: string): string {
  if (!sha) return '';
  return sha.slice(0, 7);
}

export function durationMs(start?: string, end?: string): number | null {
  if (!start) return null;
  const s = Date.parse(start);
  const e = end ? Date.parse(end) : Date.now();
  if (Number.isNaN(s) || Number.isNaN(e)) return null;
  return Math.max(0, e - s);
}

export function humanDuration(start?: string, end?: string): string {
  const ms = durationMs(start, end);
  if (ms === null) return '—';
  const s = Math.floor(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  const rem = s % 60;
  if (m < 60) return `${m}m ${rem}s`;
  const h = Math.floor(m / 60);
  return `${h}h ${m % 60}m`;
}

export function shortTime(ts?: string): string {
  if (!ts) return '—';
  const d = new Date(ts);
  if (Number.isNaN(d.getTime())) return '—';
  return d.toLocaleString();
}
