// Helpers of the template editor (plan/08 §3.6), without the DOM so that they can be tested.
import type { Template, TemplateInput } from "@/types";

// looksSecret: an env key that seems to hold a secret; secrets belong in the env file on the host.
export function looksSecret(key: string): boolean {
  return /PASS|SECRET|TOKEN|KEY/i.test(key);
}

const sizeUnits: Record<string, number> = { "": 1, B: 1, K: 1024, KB: 1024, KIB: 1024, M: 1024 ** 2, MB: 1024 ** 2, MIB: 1024 ** 2, G: 1024 ** 3, GB: 1024 ** 3, GIB: 1024 ** 3, T: 1024 ** 4, TB: 1024 ** 4, TIB: 1024 ** 4 };

// parseSize reads "4 GiB", "512M", "1.5g" or a plain number of bytes; null when it is not a size.
export function parseSize(text: string): number | null {
  const m = /^\s*(\d+(?:\.\d+)?)\s*([a-zA-Z]*)\s*$/.exec(text);
  if (!m) {
    return null;
  }
  const unit = sizeUnits[m[2].toUpperCase()];
  if (unit === undefined) {
    return null;
  }
  return Math.round(Number(m[1]) * unit);
}

// sizeText writes bytes in the largest unit that keeps a whole or one-decimal number: 4294967296 → "4 GiB".
export function sizeText(n: number): string {
  for (const [u, f] of [["TiB", 1024 ** 4], ["GiB", 1024 ** 3], ["MiB", 1024 ** 2], ["KiB", 1024]] as const) {
    if (n >= f) {
      const v = n / f;
      return (Number.isInteger(v) ? String(v) : v.toFixed(1)) + " " + u;
    }
  }
  return String(n);
}

// imageHasTag: the control plane refuses an image without a tag or digest, and the tag latest.
export function imageProblem(image: string): string | null {
  const s = image.trim();
  if (!s) return "The image is required.";
  if (s.includes("@")) return null;
  const last = s.slice(s.lastIndexOf("/") + 1);
  if (!last.includes(":")) return "The image needs a tag (e.g. automata-worker:0.4.0).";
  if (last.endsWith(":latest")) return "The tag latest is refused: name the version.";
  return null;
}

export function emptyTemplate(): TemplateInput {
  return {
    name: "",
    description: "",
    image: "",
    pullPolicy: "if-missing",
    envFile: "default",
    env: {},
    network: "host",
    extraHosts: [],
    cpus: 2,
    memoryBytes: 4 * 1024 ** 3,
    pidsLimit: 512,
    wordlistCacheMaxBytes: 1610612736,
    tmpfsSocketsBytes: 16 * 1024 ** 2,
    restartMaxRetries: 5,
    stopTimeoutSeconds: 60,
    drainTimeoutSeconds: 0,
    escalateAfterDrainTimeout: false,
    removeAfterDrain: false,
    extraLabels: {},
  };
}

// pairs and fromPairs edit a map as rows.
export function pairs(m: Record<string, string>): [string, string][] {
  return Object.entries(m);
}

export function fromPairs(rows: [string, string][]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [k, v] of rows) {
    if (k.trim()) out[k.trim()] = v;
  }
  return out;
}

// toInput keeps the fields a save sends: the API refuses unknown fields, and id and the times are not editable.
export function toInput(t: Template): TemplateInput {
  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  const { id, createdAt, updatedAt, ...input } = t;
  return input;
}
