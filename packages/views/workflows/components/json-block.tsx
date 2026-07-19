"use client";

// json-block.tsx — pretty-printed JSON for the workflow trace surfaces
// (exit_fields / artifacts / gaps / evidence / rework_context). Values are
// lenient-schema pass-throughs (unknown), so rendering always goes through
// JSON.stringify with a string fallback — never a render-time throw.

export function JsonBlock({ value }: { value: unknown }) {
  let text: string;
  try {
    text = JSON.stringify(value, null, 2) ?? String(value);
  } catch {
    text = String(value);
  }
  return (
    <pre className="max-h-64 overflow-auto rounded-md bg-muted/50 p-2 font-mono text-xs whitespace-pre-wrap">
      {text}
    </pre>
  );
}
